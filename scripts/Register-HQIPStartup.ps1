[CmdletBinding()]
param(
    [string]$Config = (Join-Path $PSScriptRoot '..\.local\controller\controller.json'),
    [string]$TaskName = 'HQIP Local Controller',
    [switch]$Remove
)
$ErrorActionPreference = 'Stop'
if ($Remove) {
    Unregister-ScheduledTask -TaskName $TaskName -Confirm:$false -ErrorAction SilentlyContinue
    Write-Output '已取消 HQIP 登录自启动。'
    return
}
$Config = [IO.Path]::GetFullPath($Config)
if (-not (Test-Path -LiteralPath $Config -PathType Leaf)) { throw '本地控制面配置不存在。' }
$scriptPath = Join-Path $PSScriptRoot 'Start-HQIP.ps1'
if ($Config.Contains('"') -or $scriptPath.Contains('"')) { throw '路径不能包含双引号。' }
$identity = [Security.Principal.WindowsIdentity]::GetCurrent().Name
$action = New-ScheduledTaskAction -Execute 'powershell.exe' -Argument ('-NoProfile -NonInteractive -ExecutionPolicy Bypass -File "' + $scriptPath + '" -Config "' + $Config + '"')
$trigger = New-ScheduledTaskTrigger -AtLogOn -User $identity
$principal = New-ScheduledTaskPrincipal -UserId $identity -LogonType Interactive -RunLevel Limited
$settings = New-ScheduledTaskSettingsSet -Hidden -StartWhenAvailable -AllowStartIfOnBatteries -DontStopIfGoingOnBatteries -MultipleInstances IgnoreNew -ExecutionTimeLimit (New-TimeSpan -Seconds 0)
Register-ScheduledTask -TaskName $TaskName -Action $action -Trigger $trigger -Principal $principal -Settings $settings -Description '登录 Windows 后启动本地 HQIP 管理服务与受限 SSH 反向隧道。' -Force | Out-Null
Write-Output '已注册当前用户登录 Windows 后自动启动 HQIP；没有保存 Windows 密码。'
