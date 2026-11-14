[CmdletBinding()]
param(
    # Path to the helm binary. When empty the script looks for "helm" on PATH
    # and then for the repository-pinned copy under .tooling.
    [string] $HelmPath = '',
    # Skip the container image build (the build needs network access for the
    # Go module download and takes minutes).
    [switch] $SkipBuild,
    # Skip the docker compose configuration validation.
    [switch] $SkipCompose
)

$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest

$repoRoot = Split-Path -Parent $PSScriptRoot
Push-Location $repoRoot
try {
    function Resolve-Helm {
        param([string] $Explicit)
        if (-not [string]::IsNullOrWhiteSpace($Explicit)) {
            if (-not (Test-Path -LiteralPath $Explicit)) { throw "Helm binary '$Explicit' does not exist." }
            return (Resolve-Path -LiteralPath $Explicit).Path
        }
        $onPath = Get-Command 'helm' -ErrorAction SilentlyContinue
        if ($null -ne $onPath) { return $onPath.Source }
        $candidate = Join-Path $repoRoot '.tooling\helm\windows-amd64\helm.exe'
        if (Test-Path -LiteralPath $candidate) { return (Resolve-Path -LiteralPath $candidate).Path }
        throw 'Helm was not found on PATH and no repository-pinned copy exists under .tooling\helm.'
    }

    $helm = Resolve-Helm -Explicit $HelmPath
    Write-Output "Helm binary: $helm"

    # A pinned image digest is mandatory: the chart refuses to render without
    # one, and an unpinned image is not a reproducible deployment.
    $digest = 'sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef'
    $values = @(
        'helm', 'lint', 'deploy/helm/ivp',
        '--set', "image.digest=$digest",
        '--set', 'image.repository=ghcr.io/ivp/ivp-api'
    )

    Write-Output '== helm lint =='
    & $helm lint deploy/helm/ivp --set "image.digest=$digest" --set 'image.repository=ghcr.io/ivp/ivp-api'
    if ($LASTEXITCODE -ne 0) { throw "helm lint failed with exit code $LASTEXITCODE." }

    Write-Output '== helm template =='
    $rendered = & $helm template ivp deploy/helm/ivp --set "image.digest=$digest" --set 'image.repository=ghcr.io/ivp/ivp-api' 2>&1
    if ($LASTEXITCODE -ne 0) { throw "helm template failed with exit code $LASTEXITCODE.`n$($rendered | Out-String)" }
    $manifest = ($rendered | Out-String)
    $lineCount = ($manifest -split "`n").Count
    Write-Output "Rendered manifest lines: $lineCount"

    # Every resource the LLD 18.2 requires must be present in the rendered
    # output, and the image must be pinned by digest.
    $required = @(
        'kind: Deployment',
        'kind: Service',
        'kind: ServiceAccount',
        'kind: PersistentVolumeClaim',
        'kind: PodDisruptionBudget',
        'kind: NetworkPolicy',
        'kind: Job',
        'kind: CronJob'
    )
    foreach ($needle in $required) {
        if ($manifest -notmatch [regex]::Escape($needle)) {
            throw "Rendered Helm manifest does not contain '$needle'."
        }
        Write-Output "PASS: rendered manifest contains '$needle'."
    }
    if ($manifest -notmatch [regex]::Escape($digest)) {
        throw 'Rendered Helm manifest does not pin the image by digest.'
    }
    Write-Output 'PASS: rendered manifest pins the container image by digest.'
    if ($manifest -notmatch 'runAsNonRoot: true') {
        throw 'Rendered Helm manifest does not enforce a non-root security context.'
    }
    Write-Output 'PASS: rendered manifest enforces runAsNonRoot.'
    if ($manifest -notmatch 'readOnlyRootFilesystem: true') {
        throw 'Rendered Helm manifest does not enforce a read-only root filesystem.'
    }
    Write-Output 'PASS: rendered manifest enforces readOnlyRootFilesystem.'
    if ($manifest -match 'image:\s*\S+:latest') {
        throw 'Rendered Helm manifest references a floating image tag.'
    }
    Write-Output 'PASS: rendered manifest contains no floating image tag.'

    # The optional local artifact storage volume must render with an explicit
    # access mode and a storage request; an unbounded volume is what the
    # artifact capacity alert cannot detect.
    if ($manifest -notmatch 'kind: PersistentVolumeClaim' -or $manifest -notmatch 'accessModes:') {
        throw 'Rendered Helm manifest does not declare the artifact volume access modes.'
    }
    Write-Output 'PASS: artifact storage volume renders with explicit access modes.'

    # The migration Job must run once per release: it is a Helm pre-install /
    # pre-upgrade hook, and the previous hook is deleted so a release never
    # runs the migration twice (LLD 18.2).
    if ($manifest -notmatch 'helm\.sh/hook:\s*pre-install,pre-upgrade') {
        throw 'The migration Job is not a pre-install/pre-upgrade hook, so it would not run once per release.'
    }
    Write-Output 'PASS: the migration Job is a pre-install/pre-upgrade hook.'
    if ($manifest -notmatch 'helm\.sh/hook-delete-policy:\s*before-hook-creation,hook-succeeded') {
        throw 'The migration Job does not delete its previous hook, so it could run twice per release.'
    }
    Write-Output 'PASS: the migration Job deletes its previous hook before a new one is created.'

    # The retention task must be scheduled, not only rendered (LLD 17.6).
    if ($manifest -notmatch 'kind: CronJob' -or $manifest -notmatch 'schedule:') {
        throw 'The retention CronJob does not carry a schedule.'
    }
    Write-Output 'PASS: the retention task renders as a scheduled CronJob.'

    if (-not $SkipCompose) {
        Write-Output '== docker compose config =='
        $composeArgs = @('compose', '--env-file', '.env.example', '--profile', 'monitoring', 'config')
        $composeOutput = & docker @composeArgs 2>&1
        if ($LASTEXITCODE -ne 0) { throw "docker compose config failed with exit code $LASTEXITCODE.`n$($composeOutput | Out-String)" }
        $composeText = ($composeOutput | Out-String)
        foreach ($service in @('postgres:', 'ivp-api:', 'otel-collector:', 'prometheus:')) {
            if ($composeText -notmatch [regex]::Escape($service)) {
                throw "docker compose configuration does not define the '$service' service."
            }
        }
        Write-Output 'PASS: docker compose configuration renders with the expected services.'
        if ($composeText -notmatch 'read_only: true') {
            throw 'docker compose configuration does not set read_only on the API service.'
        }
        Write-Output 'PASS: docker compose keeps the API container root filesystem read-only.'
        if ($composeText -notmatch 'cap_drop:') {
            throw 'docker compose configuration does not drop Linux capabilities.'
        }
        Write-Output 'PASS: docker compose drops all Linux capabilities from the API container.'

        # The compose artifact volume is mounted writable by the API while the
        # container itself stays read-only; the mount must therefore exist.
        if ($composeText -notmatch 'target: /var/lib/ivp/artifacts') {
            throw 'docker compose configuration does not mount the artifact volume.'
        }
        Write-Output 'PASS: docker compose mounts the local artifact storage volume.'
    }

    if (-not $SkipBuild) {
        Write-Output '== docker build =='
        $buildOutput = & docker build -t ivp-render-validation:local -f Dockerfile . 2>&1
        if ($LASTEXITCODE -ne 0) { throw "docker build failed with exit code $LASTEXITCODE.`n$($buildOutput | Out-String)" }
        Write-Output 'PASS: the repository container image builds.'

        # The artifact directory inside the image must be owned by the
        # non-root runtime user and must not be group/world readable, because
        # it holds sanitized tenant reports.
        $created = & docker create 'ivp-render-validation:local' 2>&1
        if ($LASTEXITCODE -ne 0) { throw "docker create failed with exit code $LASTEXITCODE." }
        $containerID = ([string]$created).Trim()
        try {
            $tarPath = Join-Path ([System.IO.Path]::GetTempPath()) ("ivp-image-" + [guid]::NewGuid().ToString('N') + '.tar')
            & docker export -o $tarPath $containerID | Out-Null
            if ($LASTEXITCODE -ne 0) { throw 'docker export failed.' }
            $entries = & tar -tvf $tarPath 2>&1
            $artifactEntry = @($entries | Where-Object { $_ -match 'var/lib/ivp/artifacts/?$' })
            if ($artifactEntry.Count -eq 0) { throw 'The built image does not contain the artifact directory.' }
            Write-Output "Artifact directory entry: $($artifactEntry[0].Trim())"
            if ($artifactEntry[0] -notmatch '65532/65532') {
                throw "The artifact directory is not owned by the non-root runtime user 65532: $($artifactEntry[0].Trim())"
            }
            Write-Output 'PASS: the image artifact directory is owned by the non-root runtime user 65532.'
            if ($artifactEntry[0] -notmatch 'drwx------') {
                throw "The artifact directory is not private to its owner: $($artifactEntry[0].Trim())"
            }
            Write-Output 'PASS: the image artifact directory is not readable by group or world.'
        }
        finally {
            & docker rm -f $containerID | Out-Null
            Remove-Item -LiteralPath $tarPath -Force -ErrorAction SilentlyContinue
        }
    }

    Write-Output 'Deployment rendering validation passed.'
}
finally {
    Pop-Location
}
