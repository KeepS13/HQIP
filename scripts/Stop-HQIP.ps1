[CmdletBinding()]
param([string]$Config = (Join-Path $PSScriptRoot '..\.local\controller\controller.json'))
$ErrorActionPreference = 'Stop'
$settings = Get-Content -Raw -LiteralPath ([IO.Path]::GetFullPath($Config)) | ConvertFrom-Json
$statePath = Join-Path $settings.stateDir 'supervisor.json'
if (-not (Test-Path -LiteralPath $statePath)) { Write-Output '没有发现本地 HQIP 监控进程记录。'; return }
$state = Get-Content -Raw -LiteralPath $statePath | ConvertFrom-Json
# Check creation times as well as PIDs so a stale state file never targets an
# unrelated process that happens to reuse a PID after a system restart.
foreach ($identity in @($state.supervisor, $state.tunnel, $state.controller)) {
    if ($null -eq $identity) { continue }
    $process = Get-Process -Id $identity.pid -ErrorAction SilentlyContinue
    try { $recordTicks = ([DateTimeOffset]$identity.startTimeUtc).UtcDateTime.Ticks } catch { continue }
    if ($null -ne $process -and $process.StartTime.ToUniversalTime().Ticks -eq $recordTicks) {
        Stop-Process -Id $process.Id -ErrorAction SilentlyContinue
    }
}
Remove-Item -LiteralPath $statePath -ErrorAction SilentlyContinue
Write-Output '已停止本地控制面和 SSH 隧道；VPS 节点及 Agent 服务继续运行。'
