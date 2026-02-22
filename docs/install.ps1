$ErrorActionPreference = "Stop"

$Repo = if ($env:OPENCORTEX_REPO) { $env:OPENCORTEX_REPO } else { "Thejuampi/opencortex" }
$PagesBase = if ($env:OPENCORTEX_PAGES_URL) { $env:OPENCORTEX_PAGES_URL } else { "https://thejuampi.github.io/opencortex" }

$release = Invoke-RestMethod -Uri "https://api.github.com/repos/$Repo/releases/latest"
$tag = $release.tag_name
if (-not $tag) { throw "Could not resolve latest release tag" }

$arch = if ([Environment]::Is64BitOperatingSystem) {
  if ($env:PROCESSOR_ARCHITECTURE -match "ARM64") { "arm64" } else { "amd64" }
} else {
  throw "Unsupported architecture"
}

$asset = "opencortex_windows_${arch}.zip"
$tmp = Join-Path $env:TEMP ("opencortex-install-" + [Guid]::NewGuid().ToString("N"))
New-Item -ItemType Directory -Force -Path $tmp | Out-Null

try {
  Write-Host "Downloading OpenCortex $tag..."
  $assetUrl = "https://github.com/$Repo/releases/download/$tag/$asset"
  $checksumUrl = "https://github.com/$Repo/releases/download/$tag/checksums.txt"
  $zipPath = Join-Path $tmp $asset
  $checksumsPath = Join-Path $tmp "checksums.txt"
  Invoke-WebRequest -Uri $assetUrl -OutFile $zipPath
  Invoke-WebRequest -Uri $checksumUrl -OutFile $checksumsPath

  $expected = (Select-String -Path $checksumsPath -Pattern (" " + [regex]::Escape($asset) + "$")).ToString().Split(" ")[0].Trim()
  $actual = (Get-FileHash -Path $zipPath -Algorithm SHA256).Hash.ToLower()
  if ($expected.ToLower() -ne $actual) { throw "Checksum mismatch" }

  Expand-Archive -Path $zipPath -DestinationPath $tmp -Force
  $binDir = Join-Path $env:USERPROFILE ".opencortex\bin"
  New-Item -ItemType Directory -Force -Path $binDir | Out-Null
  Copy-Item -Force -Path (Join-Path $tmp "opencortex.exe") -Destination (Join-Path $binDir "opencortex.exe")

  $userPath = [Environment]::GetEnvironmentVariable("Path", "User")
  if (-not $userPath.Split(";").Contains($binDir)) {
    [Environment]::SetEnvironmentVariable("Path", ($userPath.TrimEnd(";") + ";" + $binDir), "User")
    $env:Path += ";" + $binDir
  }

  & (Join-Path $binDir "opencortex.exe") init --all --silent

  $taskCmd = "`"$binDir\opencortex.exe`" server"
  schtasks /Create /F /SC ONLOGON /TN OpenCortexServer /TR $taskCmd | Out-Null

  $running = Get-CimInstance Win32_Process | Where-Object { $_.Name -eq "opencortex.exe" -and $_.CommandLine -match "server" }
  if (-not $running) {
    Start-Process -FilePath (Join-Path $binDir "opencortex.exe") -ArgumentList "server"
  }

  $readyUrl = "http://localhost:8080"
  $serverPath = Join-Path $env:USERPROFILE ".opencortex\server"
  for ($i = 0; $i -lt 30; $i++) {
    if (Test-Path $serverPath) {
      $readyUrl = (Get-Content $serverPath -ErrorAction SilentlyContinue | Select-Object -First 1).Trim()
      if (-not $readyUrl) { $readyUrl = "http://localhost:8080" }
    }
    try {
      Invoke-WebRequest -Uri "$readyUrl/healthz" -UseBasicParsing | Out-Null
      break
    } catch {}
    Start-Sleep -Seconds 1
  }

  Start-Process $readyUrl | Out-Null

  Write-Host ""
  Write-Host "✓ OpenCortex is ready."
  Write-Host "  Dashboard -> $readyUrl"
  Write-Host "  Manual setup -> $PagesBase/manual-setup.html"
}
finally {
  Remove-Item -Recurse -Force $tmp -ErrorAction SilentlyContinue
}
