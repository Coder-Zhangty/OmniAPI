param(
    [Parameter(Mandatory=$true, Position=0)]
    [ValidateSet('init','lock-images','start','stop','restart','bootstrap','healthcheck','backup','restore','test-price','acceptance')]
    [string]$Action,
    [Parameter(ValueFromRemainingArguments=$true)]
    [string[]]$RemainingArgs
)
$ErrorActionPreference = 'Stop'
$scriptFile = if ($Action -eq 'acceptance') { 'acceptance.py' } else { 'gateway.py' }
$pythonArgs = @((Join-Path $PSScriptRoot $scriptFile))
if ($Action -ne 'acceptance') { $pythonArgs += $Action }
$pythonArgs += $RemainingArgs
& python @pythonArgs
exit $LASTEXITCODE
