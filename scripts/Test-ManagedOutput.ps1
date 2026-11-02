$ErrorActionPreference = 'Stop'
Add-Type -Path (Join-Path $PSScriptRoot 'ManagedOutput.cs')
$testFolder = Join-Path ([IO.Path]::GetTempPath()) ('hqip-log-test-' + [Guid]::NewGuid().ToString('N'))
New-Item -ItemType Directory -Path $testFolder | Out-Null
$child = $null
try {
    $stdout = Join-Path $testFolder 'out.log'
    $stderr = Join-Path $testFolder 'err.log'
    [IO.File]::WriteAllText($stdout, ('x' * 100000))
    $runner = (Get-Process -Id $PID).Path
    $code = '[Console]::Out.Write((''a'' * 30000) + ''OUT-END''); [Console]::Error.Write((''b'' * 30000) + ''ERR-END'')'
    $encoded = [Convert]::ToBase64String([Text.Encoding]::Unicode.GetBytes($code))
    $child = [HQIP.ManagedOutput]::Start($runner, ('-NoProfile -NonInteractive -EncodedCommand ' + $encoded), $stdout, $stderr, 8192)
    if (-not $child.WaitForExit(15000)) { throw 'Log test child did not finish' }
    if (-not [HQIP.ManagedOutput]::WaitForOutput($child, 5000)) { throw 'Log reader did not drain' }
    if ($child.ExitCode -ne 0) { throw 'Log test child failed' }
    foreach ($path in @($stdout, ($stdout + '.1'), $stderr, ($stderr + '.1'))) {
        if (-not (Test-Path -LiteralPath $path) -or (Get-Item -LiteralPath $path).Length -gt 8192) { throw 'Log rotation exceeded bounded size' }
    }
    if (-not [IO.File]::ReadAllText($stdout).EndsWith('OUT-END') -or -not [IO.File]::ReadAllText($stderr).EndsWith('ERR-END')) { throw 'Log tail was lost' }
    Write-Output 'Bounded stdout/stderr rotation passed without child restart.'
} finally {
    if ($null -ne $child) { if (-not $child.HasExited) { $child.Kill(); $child.WaitForExit() }; $child.Dispose() }
    $resolved = [IO.Path]::GetFullPath($testFolder)
    $tempRoot = [IO.Path]::GetFullPath([IO.Path]::GetTempPath())
    if (-not $resolved.StartsWith($tempRoot, [StringComparison]::OrdinalIgnoreCase) -or [IO.Path]::GetFileName($resolved) -notlike 'hqip-log-test-*') { throw 'Unexpected test cleanup path' }
    Remove-Item -LiteralPath $resolved -Recurse -Force
}
