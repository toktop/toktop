# toktop installer for Windows.
#
#   irm https://toktop.unceas.dev/install.ps1 | iex
#
# Override the install directory with $env:TOKTOP_INSTALL_DIR (default %LOCALAPPDATA%\toktop\bin).
$ErrorActionPreference = 'Stop'

$repo = 'toktop/toktop'
$installDir = if ($env:TOKTOP_INSTALL_DIR) { $env:TOKTOP_INSTALL_DIR } else { "$env:LOCALAPPDATA\toktop\bin" }
$base = "https://github.com/$repo/releases/latest/download"

# Only windows/amd64 is shipped; it runs on ARM64 via emulation.
$asset = 'toktop_windows_amd64.zip'

$tmp = Join-Path $env:TEMP ("toktop-install-" + [guid]::NewGuid())
New-Item -ItemType Directory -Path $tmp | Out-Null
try {
	Write-Host "Downloading $asset…"
	Invoke-WebRequest -Uri "$base/$asset" -OutFile "$tmp\$asset"
	Invoke-WebRequest -Uri "$base/checksums.txt" -OutFile "$tmp\checksums.txt"

	# Optional signature check: when cosign is installed, verify that checksums.txt was
	# signed (keyless/sigstore) by toktop's release workflow. Without cosign the sha256
	# check below still runs.
	if (Get-Command cosign -ErrorAction SilentlyContinue) {
		$haveSig = $true
		try {
			Invoke-WebRequest -Uri "$base/checksums.txt.bundle" -OutFile "$tmp\checksums.txt.bundle"
		} catch { $haveSig = $false; Write-Warning 'no signature for this release - skipping (sha256 still verified)' }
		if ($haveSig) {
			Write-Host 'Verifying signature…'
			& cosign verify-blob --bundle "$tmp\checksums.txt.bundle" --certificate-identity-regexp '^https://github\.com/toktop/toktop/\.github/workflows/release\.yml@refs/tags/' --certificate-oidc-issuer 'https://token.actions.githubusercontent.com' "$tmp\checksums.txt"
			if ($LASTEXITCODE -ne 0) { throw 'signature verification failed' }
		}
	} else {
		Write-Warning 'cosign not installed - skipping signature verification (sha256 still verified)'
	}

	Write-Host 'Verifying checksum…'
	$expected = (Get-Content "$tmp\checksums.txt" | Where-Object { $_ -match "\s$([regex]::Escape($asset))$" } |
		ForEach-Object { ($_ -split '\s+')[0] })
	if (-not $expected) { throw "no checksum listed for $asset" }
	$actual = (Get-FileHash "$tmp\$asset" -Algorithm SHA256).Hash.ToLower()
	if ($expected.ToLower() -ne $actual) { throw "checksum mismatch (expected $expected, got $actual)" }

	Expand-Archive -Path "$tmp\$asset" -DestinationPath $tmp -Force
	if (-not (Test-Path "$tmp\toktop.exe")) { throw 'archive did not contain toktop.exe' }
	New-Item -ItemType Directory -Path $installDir -Force | Out-Null
	Move-Item -Path "$tmp\toktop.exe" -Destination "$installDir\toktop.exe" -Force

	Write-Host "Installed toktop to $installDir\toktop.exe"
	$userPath = [Environment]::GetEnvironmentVariable('Path', 'User')
	if ($userPath -notlike "*$installDir*") {
		[Environment]::SetEnvironmentVariable('Path', "$userPath;$installDir", 'User')
		Write-Host "Added $installDir to your user PATH — restart your shell to pick it up."
	}
	Write-Host "Run 'toktop --help' to get started."
}
finally {
	Remove-Item -Recurse -Force $tmp -ErrorAction SilentlyContinue
}
