[CmdletBinding()]
param(
    [Parameter(Mandatory = $true)]
    [string] $DatabaseUrl,
    [Parameter(Mandatory = $true)]
    [string] $BackupDirectory,
    # Number of days of full backups to keep. Older dumps are pruned after a
    # successful backup so the backup volume cannot grow without bound.
    [ValidateRange(1, 3650)]
    [int] $RetentionDays = 14,
    # Auto uses the local PostgreSQL client tools when they are installed and
    # falls back to the pinned container image otherwise.
    [ValidateSet('Auto', 'Native', 'Container')]
    [string] $ClientMode = 'Auto',
    [string] $ContainerImage = '',
    [string] $ContainerNetwork = '',
    # Host name the container uses to reach the database server. It differs
    # from the URL host when the server itself runs in a container.
    [string] $ContainerHost = '',
    # Verify that the server archives WAL and that the archive holds the
    # segments a point-in-time recovery needs.
    [switch] $VerifyWalArchive,
    # Directory the server archives WAL segments into. Only readable when the
    # archive is mounted on the same host as this script.
    [string] $WalArchiveDirectory = '',
    # Do not write anything; report what would be done.
    [switch] $DryRun
)

$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest

Import-Module (Join-Path $PSScriptRoot 'ivp-postgres.psm1') -Force

$connection = ConvertTo-PgConnection -DatabaseUrl $DatabaseUrl
$mode = $ClientMode
if ($mode -eq 'Auto') {
    $mode = if (Test-PgToolAvailable -Tool 'pg_dump') { 'Native' } else { 'Container' }
}
if ($mode -eq 'Native') {
    foreach ($tool in @('pg_dump', 'psql')) {
        if (-not (Test-PgToolAvailable -Tool $tool)) {
            throw "ClientMode Native requires the PostgreSQL client tool '$tool' on PATH."
        }
    }
}

$backupRoot = [System.IO.Path]::GetFullPath($BackupDirectory)
if (-not $DryRun) {
    if (-not (Test-Path -LiteralPath $backupRoot -PathType Container)) {
        New-Item -ItemType Directory -Path $backupRoot -Force | Out-Null
    }
}

$stamp = (Get-Date).ToUniversalTime().ToString('yyyyMMddTHHmmssZ')
$dumpName = "ivp-$stamp.dump"
$dumpPath = Join-Path $backupRoot $dumpName

Write-Output "Backup mode: $mode"
Write-Output "Backup target: $dumpPath"

if ($DryRun) {
    Write-Output "DRY RUN: would create a custom-format dump of database '$($connection.Database)' and prune dumps older than $RetentionDays days."
}
else {
    $env = @{} + $connection.Env
    if ($mode -eq 'Container') {
        # The dump is written inside the mounted backup directory so the
        # binary format is never mangled by a text pipeline.
        $env.PGHOST = if ([string]::IsNullOrWhiteSpace($ContainerHost)) { $connection.Host } else { $ContainerHost }
        $volume = @{}
        if (-not [string]::IsNullOrWhiteSpace($ContainerHost)) {
            $volume[$backupRoot] = '/backup'
        }
        $arguments = @('--format=custom', '--no-owner', '--no-privileges', '--dbname', $connection.Database)
        if (-not [string]::IsNullOrWhiteSpace($ContainerHost)) {
            $arguments += @('--file', "/backup/$dumpName")
        }
        else {
            $arguments += @('--file', $dumpPath)
        }
        Invoke-PgTool -Tool 'pg_dump' -Arguments $arguments -Environment $env -Mode 'Container' -ContainerImage $ContainerImage -ContainerNetwork $ContainerNetwork -ContainerHost $ContainerHost -VolumeMap $volume | Out-Null
    }
    else {
        $arguments = @('--format=custom', '--no-owner', '--no-privileges', '--dbname', $connection.Database, '--file', $dumpPath)
        Invoke-PgTool -Tool 'pg_dump' -Arguments $arguments -Environment $env -Mode 'Native' | Out-Null
    }

    if (-not (Test-Path -LiteralPath $dumpPath -PathType Leaf)) {
        throw "pg_dump did not produce the expected dump file '$dumpPath'."
    }
    $dumpInfo = Get-Item -LiteralPath $dumpPath
    if ($dumpInfo.Length -le 0) {
        throw "The backup dump '$dumpPath' is empty."
    }
    $digest = (Get-FileHash -LiteralPath $dumpPath -Algorithm SHA256).Hash.ToLowerInvariant()
    Set-Content -LiteralPath "$dumpPath.sha256" -Value "$digest  $dumpName" -Encoding ascii
    Write-Output "PASS: full backup created ($([math]::Round($dumpInfo.Length / 1MB, 2)) MiB, sha256 $digest)."

    # Retention keeps the backup volume bounded. Pruning only removes dumps
    # this script produced and only after a newer dump exists.
    $cutoff = (Get-Date).ToUniversalTime().AddDays(-$RetentionDays)
    $pruned = 0
    foreach ($file in Get-ChildItem -LiteralPath $backupRoot -Filter 'ivp-*.dump' -File) {
        if ($file.LastWriteTimeUtc -lt $cutoff -and $file.FullName -ne $dumpPath) {
            Remove-Item -LiteralPath $file.FullName -Force
            Remove-Item -LiteralPath "$($file.FullName).sha256" -Force -ErrorAction SilentlyContinue
            $pruned++
        }
    }
    Write-Output "Retention: pruned $pruned dump(s) older than $RetentionDays days."
}

if ($VerifyWalArchive) {
    Write-Output '== WAL archive verification =='
    $settings = Invoke-PgScalar -Sql "SELECT name||'='||setting FROM pg_settings WHERE name IN ('archive_mode','archive_command','wal_level','archive_timeout') ORDER BY name" -Connection $connection -Mode $mode -ContainerImage $ContainerImage -ContainerNetwork $ContainerNetwork -ContainerHost $ContainerHost
    foreach ($line in $settings) {
        if ([string]::IsNullOrWhiteSpace($line)) { continue }
        Write-Output "  $line"
    }
    $settingsMap = @{}
    foreach ($line in $settings) {
        if ($line -match '^([^=]+)=(.*)$') { $settingsMap[$matches[1]] = $matches[2] }
    }
    if ($settingsMap['archive_mode'] -ne 'on') {
        throw "WAL archiving is not enabled (archive_mode=$($settingsMap['archive_mode'])); point-in-time recovery is not possible."
    }
    Write-Output 'PASS: WAL archiving is enabled on the database server.'
    if ([string]::IsNullOrWhiteSpace($settingsMap['archive_command'])) {
        throw 'WAL archiving is enabled but no archive_command is configured.'
    }
    Write-Output 'PASS: the database server archives WAL segments to a configured destination.'
    if ($settingsMap['wal_level'] -notin @('replica', 'logical')) {
        throw "wal_level is '$($settingsMap['wal_level'])', which does not support WAL-based recovery."
    }
    Write-Output 'PASS: wal_level supports WAL-based recovery.'

    if (-not [string]::IsNullOrWhiteSpace($WalArchiveDirectory)) {
        $archiveRoot = [System.IO.Path]::GetFullPath($WalArchiveDirectory)
        if (-not (Test-Path -LiteralPath $archiveRoot -PathType Container)) {
            throw "The WAL archive directory '$archiveRoot' does not exist."
        }
        $segments = @(Get-ChildItem -LiteralPath $archiveRoot -Filter '0000000*' -File -ErrorAction SilentlyContinue)
        if ($segments.Count -eq 0) {
            throw "The WAL archive directory '$archiveRoot' holds no archived WAL segments."
        }
        $newest = ($segments | Sort-Object LastWriteTimeUtc -Descending | Select-Object -First 1)
        Write-Output "PASS: the WAL archive holds $($segments.Count) segment(s); the newest is $($newest.Name) written $($newest.LastWriteTimeUtc.ToString('u'))."
    }
    else {
        Write-Output 'NOTE: pass -WalArchiveDirectory to also verify that archived WAL segments exist on this host.'
    }
}

Write-Output 'PostgreSQL backup completed.'
