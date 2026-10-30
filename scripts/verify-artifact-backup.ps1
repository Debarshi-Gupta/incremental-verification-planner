param(
  [Parameter(Mandatory = $true)][string]$ArtifactDirectory,
  [Parameter(Mandatory = $true)][string]$BackupPath
)

$ErrorActionPreference = 'Stop'

function Get-ArtifactManifest([string]$Root) {
  $resolvedRoot = [System.IO.Path]::GetFullPath($Root).TrimEnd([System.IO.Path]::DirectorySeparatorChar, [System.IO.Path]::AltDirectorySeparatorChar)
  $manifest = @(
    Get-ChildItem -LiteralPath $resolvedRoot -File -Recurse -Force | ForEach-Object {
      if (($_.Attributes -band [System.IO.FileAttributes]::ReparsePoint) -ne 0) {
        throw "Artifact backup verification does not follow reparse points: $($_.FullName)"
      }
      $relative = [System.IO.Path]::GetRelativePath($resolvedRoot, $_.FullName).Replace('\', '/')
      [PSCustomObject]@{
        Path = $relative
        Length = $_.Length
        SHA256 = (Get-FileHash -LiteralPath $_.FullName -Algorithm SHA256).Hash.ToLowerInvariant()
      }
    } | Sort-Object Path
  )
  return ,$manifest
}

$source = (Resolve-Path -LiteralPath $ArtifactDirectory).Path
if (-not (Test-Path -LiteralPath $source -PathType Container)) {
  throw 'ArtifactDirectory must name an existing directory.'
}
$backup = [System.IO.Path]::GetFullPath($BackupPath)
$relativeBackup = [System.IO.Path]::GetRelativePath($source, $backup)
if (-not [System.IO.Path]::IsPathRooted($relativeBackup) -and -not $relativeBackup.StartsWith('..', [System.StringComparison]::Ordinal)) {
  throw 'BackupPath must be outside ArtifactDirectory.'
}
if (Test-Path -LiteralPath $backup) {
  throw 'BackupPath already exists; choose a new output path.'
}
$backupParent = Split-Path -Parent $backup
if (-not (Test-Path -LiteralPath $backupParent -PathType Container)) {
  New-Item -ItemType Directory -Path $backupParent -Force | Out-Null
}

$before = Get-ArtifactManifest $source
$tempRoot = [System.IO.Path]::GetFullPath([System.IO.Path]::GetTempPath())
$restore = Join-Path $tempRoot ("ivp-artifact-restore-" + [guid]::NewGuid().ToString('N'))
New-Item -ItemType Directory -Path $restore | Out-Null
try {
  Compress-Archive -Path (Join-Path $source '*') -DestinationPath $backup -CompressionLevel Optimal
  Expand-Archive -LiteralPath $backup -DestinationPath $restore
  $after = Get-ArtifactManifest $restore
  $beforeJson = ConvertTo-Json -InputObject $before -Depth 4 -Compress
  $afterJson = ConvertTo-Json -InputObject $after -Depth 4 -Compress
  if ($beforeJson -cne $afterJson) {
    throw 'Restored artifact manifest does not match the source object manifest.'
  }
  Write-Output "PASS: artifact backup restored and verified ($($before.Count) objects)."
} finally {
  $restoreFull = [System.IO.Path]::GetFullPath($restore)
  if (-not $restoreFull.StartsWith($tempRoot, [System.StringComparison]::OrdinalIgnoreCase)) {
    throw 'Refusing to remove restore verification directory outside the system temporary directory.'
  }
  Remove-Item -LiteralPath $restoreFull -Recurse -Force
}
