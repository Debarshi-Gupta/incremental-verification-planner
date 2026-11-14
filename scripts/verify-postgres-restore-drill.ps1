[CmdletBinding()]
param(
    [Parameter(Mandatory = $true)]
    [string] $DatabaseUrl,
    [Parameter(Mandatory = $true)]
    [string] $BackupDirectory,
    # Only restore dumps that are at least this many hours old. The drill then
    # proves that a backup taken in the past can actually be restored.
    [ValidateRange(0, 8760)]
    [int] $MinimumAgeHours = 24,
    [ValidateSet('Auto', 'Native', 'Container')]
    [string] $ClientMode = 'Auto',
    [string] $ContainerImage = '',
    [string] $ContainerNetwork = '',
    [string] $ContainerHost = '',
    # Directory where the drill writes its evidence file.
    [string] $EvidenceDirectory = ''
)

$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest

Import-Module (Join-Path $PSScriptRoot 'ivp-postgres.psm1') -Force

$connection = ConvertTo-PgConnection -DatabaseUrl $DatabaseUrl
$mode = $ClientMode
if ($mode -eq 'Auto') {
    $mode = if (Test-PgToolAvailable -Tool 'pg_restore') { 'Native' } else { 'Container' }
}
if ($mode -eq 'Native') {
    foreach ($tool in @('pg_restore', 'createdb', 'dropdb', 'psql')) {
        if (-not (Test-PgToolAvailable -Tool $tool)) {
            throw "ClientMode Native requires the PostgreSQL client tool '$tool' on PATH."
        }
    }
}

$backupRoot = [System.IO.Path]::GetFullPath($BackupDirectory)
if (-not (Test-Path -LiteralPath $backupRoot -PathType Container)) {
    throw "BackupDirectory '$backupRoot' does not exist."
}
$candidates = @(Get-ChildItem -LiteralPath $backupRoot -Filter 'ivp-*.dump' -File | Sort-Object LastWriteTimeUtc -Descending)
if ($candidates.Count -eq 0) {
    throw "No backup dump was found in '$backupRoot'."
}
$cutoff = (Get-Date).ToUniversalTime().AddHours(-$MinimumAgeHours)
$dump = $candidates | Where-Object { $_.LastWriteTimeUtc -lt $cutoff } | Select-Object -First 1
if ($null -eq $dump) {
    if ($MinimumAgeHours -eq 0) {
        $dump = $candidates[0]
    }
    else {
        throw "No backup dump in '$backupRoot' is at least $MinimumAgeHours hour(s) old, so the restore drill would not prove that an aged backup restores."
    }
}
Write-Output "Restoring backup: $($dump.FullName) (written $($dump.LastWriteTimeUtc.ToString('u')))"

# The dump digest recorded next to the backup must match the restored file,
# so a corrupted or truncated backup is rejected before it is restored.
$digestFile = "$($dump.FullName).sha256"
if (Test-Path -LiteralPath $digestFile -PathType Leaf) {
    $expected = ((Get-Content -LiteralPath $digestFile -TotalCount 1) -split '\s+')[0].ToLowerInvariant()
    $actual = (Get-FileHash -LiteralPath $dump.FullName -Algorithm SHA256).Hash.ToLowerInvariant()
    if ($expected -ne $actual) {
        throw "The backup digest does not match the recorded checksum (expected $expected, got $actual)."
    }
    Write-Output "PASS: the backup digest matches the recorded SHA-256 checksum."
}

$restoreDatabase = 'ivp_restore_drill_' + [guid]::NewGuid().ToString('N')
$created = $false
$started = (Get-Date).ToUniversalTime()
try {
    $env = @{} + $connection.Env
    if ($mode -eq 'Container') {
        $env.PGHOST = if ([string]::IsNullOrWhiteSpace($ContainerHost)) { $connection.Host } else { $ContainerHost }
    }

    Invoke-PgTool -Tool 'createdb' -Arguments @('--maintenance-db=postgres', '--template=template0', $restoreDatabase) -Environment $env -Mode $mode -ContainerImage $ContainerImage -ContainerNetwork $ContainerNetwork -ContainerHost $ContainerHost | Out-Null
    $created = $true
    Write-Output 'PASS: an isolated restore database was created.'

    if ($mode -eq 'Container') {
        $volume = @{ $backupRoot = '/backup' }
        Invoke-PgTool -Tool 'pg_restore' -Arguments @('--exit-on-error', '--no-owner', '--no-privileges', '--dbname', $restoreDatabase, "/backup/$($dump.Name)") -Environment $env -Mode 'Container' -ContainerImage $ContainerImage -ContainerNetwork $ContainerNetwork -ContainerHost $ContainerHost -VolumeMap $volume | Out-Null
    }
    else {
        Invoke-PgTool -Tool 'pg_restore' -Arguments @('--exit-on-error', '--no-owner', '--no-privileges', '--dbname', $restoreDatabase, $dump.FullName) -Environment $env -Mode 'Native' | Out-Null
    }
    Write-Output 'PASS: the backup was restored into the isolated database without errors.'

    $restoreConnection = @{} + $connection
    $restoreConnection.Database = $restoreDatabase
    $restoreConnection.Env = @{} + $env
    $restoreConnection.Env.PGDATABASE = $restoreDatabase

    $migrationCount = Invoke-PgScalar -Sql 'SELECT count(*) FROM goose_db_version WHERE is_applied' -Connection $restoreConnection -Mode $mode -ContainerImage $ContainerImage -ContainerNetwork $ContainerNetwork -ContainerHost $ContainerHost
    if ([int]$migrationCount -lt 1) {
        throw 'The restored database contains no applied Goose migration.'
    }
    Write-Output "PASS: the restored database carries $migrationCount applied migration(s)."

    $tableCount = Invoke-PgScalar -Sql "SELECT count(*) FROM information_schema.tables WHERE table_schema='public'" -Connection $restoreConnection -Mode $mode -ContainerImage $ContainerImage -ContainerNetwork $ContainerNetwork -ContainerHost $ContainerHost
    if ([int]$tableCount -lt 10) {
        throw "The restored database holds only $tableCount public tables; the IVP schema is incomplete."
    }
    Write-Output "PASS: the restored database holds $tableCount IVP schema table(s)."

    # The tenant boundary must survive the restore: the row-level security
    # policies that isolate tenants are part of the schema, not of the data.
    $policyCount = Invoke-PgScalar -Sql "SELECT count(*) FROM pg_policies WHERE schemaname='public'" -Connection $restoreConnection -Mode $mode -ContainerImage $ContainerImage -ContainerNetwork $ContainerNetwork -ContainerHost $ContainerHost
    if ([int]$policyCount -lt 1) {
        throw 'The restored database has no row-level security policies; tenant isolation was lost.'
    }
    Write-Output "PASS: the restored database carries $policyCount row-level security policy/policies."

    $rlsCount = Invoke-PgScalar -Sql "SELECT count(*) FROM pg_tables WHERE schemaname='public' AND rowsecurity" -Connection $restoreConnection -Mode $mode -ContainerImage $ContainerImage -ContainerNetwork $ContainerNetwork -ContainerHost $ContainerHost
    if ([int]$rlsCount -lt 5) {
        throw "Only $rlsCount restored table(s) have row-level security enabled."
    }
    Write-Output "PASS: $rlsCount restored table(s) have row-level security enabled."

    $tenantCount = Invoke-PgScalar -Sql 'SELECT count(*) FROM tenants' -Connection $restoreConnection -Mode $mode -ContainerImage $ContainerImage -ContainerNetwork $ContainerNetwork -ContainerHost $ContainerHost
    Write-Output "PASS: the restored database holds $tenantCount tenant row(s)."

    $finished = (Get-Date).ToUniversalTime()
    $evidence = [ordered]@{
        checked_at_utc   = $finished.ToString('o')
        backup_file      = $dump.Name
        backup_written_utc = $dump.LastWriteTimeUtc.ToString('o')
        restore_database = $restoreDatabase
        client_mode      = $mode
        applied_migrations = [int]$migrationCount
        schema_tables    = [int]$tableCount
        rls_policies     = [int]$policyCount
        rls_tables       = [int]$rlsCount
        tenant_rows      = [int]$tenantCount
        duration_seconds = [math]::Round(($finished - $started).TotalSeconds, 3)
        result           = 'PASS'
    }
    $evidenceJson = $evidence | ConvertTo-Json -Depth 4
    Write-Output $evidenceJson

    if (-not [string]::IsNullOrWhiteSpace($EvidenceDirectory)) {
        $evidenceRoot = [System.IO.Path]::GetFullPath($EvidenceDirectory)
        if (-not (Test-Path -LiteralPath $evidenceRoot -PathType Container)) {
            New-Item -ItemType Directory -Path $evidenceRoot -Force | Out-Null
        }
        $evidencePath = Join-Path $evidenceRoot ("restore-drill-" + $finished.ToString('yyyyMMddTHHmmssZ') + '.json')
        Set-Content -LiteralPath $evidencePath -Value $evidenceJson -Encoding utf8
        Write-Output "Restore drill evidence written to $evidencePath"
    }
}
finally {
    if ($created) {
        $env = @{} + $connection.Env
        if ($mode -eq 'Container') {
            $env.PGHOST = if ([string]::IsNullOrWhiteSpace($ContainerHost)) { $connection.Host } else { $ContainerHost }
        }
        Invoke-PgTool -Tool 'dropdb' -Arguments @('--maintenance-db=postgres', '--if-exists', $restoreDatabase) -Environment $env -Mode $mode -ContainerImage $ContainerImage -ContainerNetwork $ContainerNetwork -ContainerHost $ContainerHost | Out-Null
        Write-Output "Removed the isolated restore database $restoreDatabase."
    }
}

Write-Output 'PostgreSQL restore drill completed.'
