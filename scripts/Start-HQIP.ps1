[CmdletBinding()]
param(
    [string]$Config = (Join-Path $PSScriptRoot '..\.local\controller\controller.json'),
    [switch]$Watch
)
$ErrorActionPreference = 'Stop'
$Config = [IO.Path]::GetFullPath($Config)
$settings = Get-Content -Raw -LiteralPath $Config | ConvertFrom-Json
if ($settings.phase -notin @('restored', 'active')) { throw '请先完成 prepare 和 cutover，恢复本地数据库后再启动。' }
if (-not (Test-Path -LiteralPath $settings.binary -PathType Leaf)) { throw '找不到本地 HQIP 二进制，请先构建。' }
if (-not (Test-Path -LiteralPath (Join-Path $settings.dataDir 'hqip.db') -PathType Leaf)) { throw '本地数据库尚未恢复。' }
$statePath = Join-Path $settings.stateDir 'supervisor.json'

function Quote-ProcessArgument([string]$Value) {
    if ($Value -notmatch '[\s"]' -and $Value.Length -gt 0) { return $Value }
    $escaped = [regex]::Replace($Value, '(\\*)"', '$1$1\"')
    $escaped = [regex]::Replace($escaped, '(\\+)$', '$1$1')
    return '"' + $escaped + '"'
}
function Process-Identity($Process) {
    return @{ pid = $Process.Id; startTimeUtc = $Process.StartTime.ToUniversalTime().ToString('o') }
}
function Find-TrackedProcess($Identity) {
    if ($null -eq $Identity) { return $null }
    $process = Get-Process -Id $Identity.pid -ErrorAction SilentlyContinue
    if ($null -eq $process) { return $null }
    # PowerShell 7 may deserialize ISO JSON into DateTime; compare UTC ticks,
    # not the locale-formatted string representation of that object.
    try { $recordTicks = ([DateTimeOffset]$Identity.startTimeUtc).UtcDateTime.Ticks } catch { return $null }
    if ($process.StartTime.ToUniversalTime().Ticks -ne $recordTicks) { return $null }
    return $process
}
if (-not $Watch) {
    if (Test-Path -LiteralPath $statePath) {
        $old = Get-Content -Raw -LiteralPath $statePath | ConvertFrom-Json
        if ($null -ne (Find-TrackedProcess $old.supervisor)) { Write-Output 'HQIP 本地管理和隧道监控已运行。'; return }
    }
    $arguments = @('-NoProfile', '-NonInteractive', '-ExecutionPolicy', 'Bypass', '-File', $PSCommandPath, '-Config', $Config, '-Watch')
    $argumentLine = ($arguments | ForEach-Object { Quote-ProcessArgument $_ }) -join ' '
    $runner = Join-Path $PSHOME 'powershell.exe'
    if (-not (Test-Path -LiteralPath $runner)) { $runner = Join-Path $PSHOME 'pwsh.exe' }
    Start-Process -FilePath $runner -ArgumentList $argumentLine -WindowStyle Hidden | Out-Null
    Write-Output '已启动本地 HQIP 监控；管理员页面 http://127.0.0.1:8080，公网仅转发客户端和 Agent API。'
    return
}

$hashAlgorithm = [Security.Cryptography.SHA256]::Create()
try { $instance = [BitConverter]::ToString($hashAlgorithm.ComputeHash([Text.Encoding]::UTF8.GetBytes($Config))).Replace('-', '') } finally { $hashAlgorithm.Dispose() }
$created = $false
$mutex = New-Object Threading.Mutex($true, ('Local\HQIP-' + $instance), [ref]$created)
if (-not $created) { $mutex.Dispose(); return }
if (Test-Path -LiteralPath $statePath) {
    $previous = Get-Content -Raw -LiteralPath $statePath | ConvertFrom-Json
    foreach ($identity in @($previous.tunnel, $previous.controller)) {
        $leftover = Find-TrackedProcess $identity
        if ($null -ne $leftover) { Stop-Process -Id $leftover.Id -ErrorAction SilentlyContinue }
    }
}
$controller = $null
$tunnel = $null
$sshExecutable = (Get-Command ssh.exe -ErrorAction Stop).Source
$logDirectory = Join-Path $settings.stateDir 'logs'
New-Item -ItemType Directory -Path $logDirectory -Force | Out-Null
$supervisorLog = Join-Path $logDirectory 'supervisor.log'
Add-Type -Path (Join-Path $PSScriptRoot 'ManagedOutput.cs')
function Rotate-Log([string]$Path) {
    if ((Test-Path -LiteralPath $Path) -and (Get-Item -LiteralPath $Path).Length -ge 10MB) {
        Move-Item -LiteralPath $Path -Destination ($Path + '.1') -Force
    }
}
function Start-Managed([string]$Executable, [string[]]$Arguments, [string]$Name) {
    $stdout = Join-Path $logDirectory ($Name + '.out.log')
    $stderr = Join-Path $logDirectory ($Name + '.err.log')
    $argumentLine = ($Arguments | ForEach-Object { Quote-ProcessArgument $_ }) -join ' '
    return [HQIP.ManagedOutput]::Start($Executable, $argumentLine, $stdout, $stderr, 10MB)
}
try {
    while ($true) {
        try {
            if ($null -eq $controller -or $controller.HasExited) {
                if ($null -ne $controller) { [HQIP.ManagedOutput]::WaitForOutput($controller, 1000) | Out-Null; $controller.Dispose() }
                $arguments = @('serve', '--data-dir', $settings.dataDir, '--listen', $settings.adminListen, '--public-listen', $settings.publicListen, '--admin-origin', $settings.adminOrigin, '--public-url', $settings.publicUrl, '--artifact-dir', $settings.artifactDir)
                $controller = Start-Managed $settings.binary $arguments 'controller'
            }
            if ($null -eq $tunnel -or $tunnel.HasExited) {
                if ($null -ne $tunnel) { [HQIP.ManagedOutput]::WaitForOutput($tunnel, 1000) | Out-Null; $tunnel.Dispose() }
                $tunnel = Start-Managed $sshExecutable @('-N', '-T', '-F', $settings.sshConfig, 'hqip-controller') 'tunnel'
            }
            $state = @{ supervisor = (Process-Identity (Get-Process -Id $PID)); controller = (Process-Identity $controller); tunnel = (Process-Identity $tunnel); config = $Config }
            $json = $state | ConvertTo-Json -Depth 4
            [IO.File]::WriteAllText(($statePath + '.new'), $json, (New-Object Text.UTF8Encoding($false)))
            Move-Item -LiteralPath ($statePath + '.new') -Destination $statePath -Force
        } catch {
            Rotate-Log $supervisorLog
            Add-Content -LiteralPath $supervisorLog -Value ((Get-Date).ToUniversalTime().ToString('o') + ' ' + $_.Exception.Message)
        }
        Start-Sleep -Seconds 5
    }
} finally {
    foreach ($process in @($tunnel, $controller)) { if ($null -ne $process -and -not $process.HasExited) { Stop-Process -Id $process.Id -ErrorAction SilentlyContinue } }
    if (Test-Path -LiteralPath $statePath) { Remove-Item -LiteralPath $statePath }
    $mutex.ReleaseMutex()
    $mutex.Dispose()
}
