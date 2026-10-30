[CmdletBinding()]
param(
    [Parameter(Mandatory = $true)]
    [string] $DatabaseUrl
)

$ErrorActionPreference = 'Stop'

foreach ($tool in @('pg_dump', 'pg_restore', 'createdb', 'dropdb', 'psql')) {
    if (-not (Get-Command $tool -ErrorAction SilentlyContinue)) {
        throw "Required PostgreSQL client '$tool' is not on PATH."
    }
}

$uri = [System.Uri]::new($DatabaseUrl)
if ($uri.Scheme -notin @('postgres', 'postgresql') -or [string]::IsNullOrWhiteSpace($uri.Host) -or [string]::IsNullOrWhiteSpace($uri.AbsolutePath.Trim('/'))) {
    throw 'DatabaseUrl must be a postgres:// or postgresql:// URL with host and database.'
}

$query = @{}
foreach ($part in $uri.Query.TrimStart('?').Split('&', [System.StringSplitOptions]::RemoveEmptyEntries)) {
    $pair = $part.Split('=', 2)
    $query[[System.Uri]::UnescapeDataString($pair[0])] = if ($pair.Length -gt 1) { [System.Uri]::UnescapeDataString($pair[1]) } else { '' }
}

$env:PGHOST = $uri.Host
$env:PGPORT = if ($uri.IsDefaultPort) { '5432' } else { [string]$uri.Port }
$env:PGUSER = [System.Uri]::UnescapeDataString($uri.UserInfo.Split(':', 2)[0])
$env:PGPASSWORD = if ($uri.UserInfo.Contains(':')) { [System.Uri]::UnescapeDataString($uri.UserInfo.Split(':', 2)[1]) } else { '' }
$env:PGSSLMODE = if ($query.ContainsKey('sslmode')) { $query['sslmode'] } else { 'prefer' }
$sourceDatabase = [System.Uri]::UnescapeDataString($uri.AbsolutePath.TrimStart('/'))
$restoreDatabase = 'ivp_restore_check_' + [guid]::NewGuid().ToString('N')
$backupFile = Join-Path ([System.IO.Path]::GetTempPath()) ($restoreDatabase + '.dump')
$created = $false

try {
    & pg_dump --format=custom --no-owner --no-privileges --file=$backupFile --dbname=$sourceDatabase
    if ($LASTEXITCODE -ne 0) { throw "pg_dump failed with exit code $LASTEXITCODE." }

    & createdb --maintenance-db=postgres --template=template0 $restoreDatabase
    if ($LASTEXITCODE -ne 0) { throw "Could not create isolated restore database (exit code $LASTEXITCODE)." }
    $created = $true

    & pg_restore --exit-on-error --no-owner --no-privileges --dbname=$restoreDatabase $backupFile
    if ($LASTEXITCODE -ne 0) { throw "pg_restore failed with exit code $LASTEXITCODE." }

    $migrationCount = & psql --no-psqlrc --tuples-only --no-align --dbname=$restoreDatabase --command="SELECT count(*) FROM goose_db_version WHERE is_applied"
    if ($LASTEXITCODE -ne 0 -or [int]::Parse(($migrationCount | Out-String).Trim()) -lt 1) {
        throw 'Restore verification did not find an applied Goose migration.'
    }
    $tenantCount = & psql --no-psqlrc --tuples-only --no-align --dbname=$restoreDatabase --command='SELECT count(*) FROM tenants'
    if ($LASTEXITCODE -ne 0) { throw 'Restore verification could not query IVP tenant data.' }

    Write-Output "Backup restore verification passed. Applied migrations: $($migrationCount.Trim()); tenant rows: $($tenantCount.Trim())."
}
finally {
    if ($created) {
        & dropdb --maintenance-db=postgres --if-exists $restoreDatabase
        if ($LASTEXITCODE -ne 0) { Write-Warning "Could not remove isolated restore database $restoreDatabase." }
    }
    Remove-Item -LiteralPath $backupFile -Force -ErrorAction SilentlyContinue
    Remove-Item Env:PGPASSWORD -ErrorAction SilentlyContinue
}
