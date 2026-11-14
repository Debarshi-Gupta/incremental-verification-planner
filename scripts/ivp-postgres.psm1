# IVP PostgreSQL client helper module.
#
# The module lets the backup/restore verification scripts run the PostgreSQL
# client tools (pg_dump, pg_restore, createdb, dropdb, psql) either from a
# local PostgreSQL client installation or from a pinned container image. The
# container mode exists because many operator hosts (and the CI runners used
# by this repository) do not ship the PostgreSQL client tools at all, while
# the container image is pinned by digest and therefore reproducible.
#
# Every function is side-effect free apart from the explicit database
# operations the calling script requests.

Set-StrictMode -Version Latest

$script:DefaultContainerImage = 'postgres@sha256:1d04b9ba1d4996401f2552b51beda8187f175c0645c091e4781134fc9c9a3eef'

function Get-PgContainerImage {
    <#
    .SYNOPSIS
    Returns the pinned PostgreSQL container image used for client tools.
    #>
    [CmdletBinding()]
    param(
        [string] $Image = ''
    )
    if ([string]::IsNullOrWhiteSpace($Image)) {
        return $script:DefaultContainerImage
    }
    return $Image
}

function ConvertTo-PgConnection {
    <#
    .SYNOPSIS
    Splits a postgres:// URL into its parts and the environment variables the
    PostgreSQL client tools expect.
    .DESCRIPTION
    Throws when the URL is not a usable PostgreSQL connection string.
    #>
    [CmdletBinding()]
    [OutputType([hashtable])]
    param(
        [Parameter(Mandatory = $true)]
        [string] $DatabaseUrl
    )

    $uri = [System.Uri]::new($DatabaseUrl)
    if ($uri.Scheme -notin @('postgres', 'postgresql') -or [string]::IsNullOrWhiteSpace($uri.Host) -or [string]::IsNullOrWhiteSpace($uri.AbsolutePath.Trim('/'))) {
        throw 'DatabaseUrl must be a postgres:// or postgresql:// URL with host and database.'
    }

    $query = @{}
    foreach ($part in $uri.Query.TrimStart('?').Split('&', [System.StringSplitOptions]::RemoveEmptyEntries)) {
        $pair = $part.Split('=', 2)
        $query[[System.Uri]::UnescapeDataString($pair[0])] = if ($pair.Length -gt 1) { [System.Uri]::UnescapeDataString($pair[1]) } else { '' }
    }

    $userInfo = $uri.UserInfo.Split(':', 2)
    $environment = @{
        PGHOST     = $uri.Host
        PGPORT     = if ($uri.IsDefaultPort) { '5432' } else { [string]$uri.Port }
        PGUSER     = [System.Uri]::UnescapeDataString($userInfo[0])
        PGPASSWORD = if ($userInfo.Length -gt 1) { [System.Uri]::UnescapeDataString($userInfo[1]) } else { '' }
        PGSSLMODE  = if ($query.ContainsKey('sslmode')) { $query['sslmode'] } else { 'prefer' }
    }

    return @{
        Host      = $environment.PGHOST
        Port      = $environment.PGPORT
        User      = $environment.PGUSER
        Password  = $environment.PGPASSWORD
        SslMode   = $environment.PGSSLMODE
        Database  = [System.Uri]::UnescapeDataString($uri.AbsolutePath.TrimStart('/'))
        Env       = $environment
    }
}

function Test-PgToolAvailable {
    <#
    .SYNOPSIS
    Reports whether a PostgreSQL client tool is installed on the host.
    #>
    [CmdletBinding()]
    [OutputType([bool])]
    param(
        [Parameter(Mandatory = $true)]
        [string] $Tool
    )
    return $null -ne (Get-Command $Tool -ErrorAction SilentlyContinue)
}

function Invoke-PgTool {
    <#
    .SYNOPSIS
    Runs a PostgreSQL client tool and returns its standard output.
    .DESCRIPTION
    Mode 'Native' executes the tool from PATH. Mode 'Container' executes the
    same tool inside a pinned PostgreSQL container image. The function throws
    when the tool reports a failure so that callers never mistake a partial
    operation for a successful one.
    #>
    [CmdletBinding()]
    [OutputType([string[]])]
    param(
        [Parameter(Mandatory = $true)]
        [string] $Tool,
        [string[]] $Arguments = @(),
        [hashtable] $Environment = @{},
        [ValidateSet('Native', 'Container')]
        [string] $Mode = 'Native',
        [string] $ContainerImage = '',
        [string] $ContainerNetwork = '',
        [hashtable] $VolumeMap = @{},
        [string] $ContainerHost = '',
        [int] $TimeoutSeconds = 600
    )

    $image = Get-PgContainerImage -Image $ContainerImage
    if ($Mode -eq 'Container') {
        $dockerArgs = @('run', '--rm')
        if (-not [string]::IsNullOrWhiteSpace($ContainerNetwork)) {
            $dockerArgs += @('--network', $ContainerNetwork)
        }
        foreach ($key in ($Environment.Keys | Sort-Object)) {
            $dockerArgs += @('-e', ("{0}={1}" -f $key, $Environment[$key]))
        }
        foreach ($key in ($VolumeMap.Keys | Sort-Object)) {
            $dockerArgs += @('-v', ("{0}:{1}" -f $key, $VolumeMap[$key]))
        }
        $dockerArgs += $image
        $dockerArgs += $Tool
        $dockerArgs += $Arguments

        $output = & docker @dockerArgs 2>&1
        if ($LASTEXITCODE -ne 0) {
            throw ("PostgreSQL tool '{0}' failed in container mode with exit code {1}: {2}" -f $Tool, $LASTEXITCODE, ($output | Out-String).Trim())
        }
        return @($output | ForEach-Object { [string]$_ })
    }

    $saved = @{}
    try {
        foreach ($key in $Environment.Keys) {
            $saved[$key] = [System.Environment]::GetEnvironmentVariable($key)
            [System.Environment]::SetEnvironmentVariable($key, [string]$Environment[$key])
        }
        $output = & $Tool @Arguments 2>&1
        if ($LASTEXITCODE -ne 0) {
            throw ("PostgreSQL tool '{0}' failed with exit code {1}: {2}" -f $Tool, $LASTEXITCODE, ($output | Out-String).Trim())
        }
        return @($output | ForEach-Object { [string]$_ })
    }
    finally {
        foreach ($key in $saved.Keys) {
            [System.Environment]::SetEnvironmentVariable($key, $saved[$key])
        }
    }
}

function Invoke-PgScalar {
    <#
    .SYNOPSIS
    Runs a scalar SQL statement and returns the trimmed first column value.
    #>
    [CmdletBinding()]
    [OutputType([string])]
    param(
        [Parameter(Mandatory = $true)]
        [string] $Sql,
        [Parameter(Mandatory = $true)]
        [hashtable] $Connection,
        [ValidateSet('Native', 'Container')]
        [string] $Mode = 'Native',
        [string] $ContainerImage = '',
        [string] $ContainerNetwork = '',
        [string] $ContainerHost = ''
    )

    $env = @{} + $Connection.Env
    if ($Mode -eq 'Container' -and -not [string]::IsNullOrWhiteSpace($ContainerHost)) {
        $env.PGHOST = $ContainerHost
    }
    $args = @('--no-psqlrc', '--tuples-only', '--no-align', '--dbname', $Connection.Database, '--command', $Sql)
    $output = Invoke-PgTool -Tool 'psql' -Arguments $args -Environment $env -Mode $Mode -ContainerImage $ContainerImage -ContainerNetwork $ContainerNetwork -ContainerHost $ContainerHost
    $value = ($output | Out-String).Trim()
    return $value
}

Export-ModuleMember -Function Get-PgContainerImage, ConvertTo-PgConnection, Test-PgToolAvailable, Invoke-PgTool, Invoke-PgScalar
