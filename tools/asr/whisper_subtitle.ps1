# Usage: .\whisper_subtitle.ps1 -InputPath 'D:\a.mp4' -OutputVtt 'D:\out.vtt' -- --whisper-model base
param(
    [Parameter(Mandatory = $true)][string]$InputPath,
    [Parameter(Mandatory = $true)][string]$OutputVtt,
    [Parameter(ValueFromRemainingArguments = $true)][string[]]$Extra = @()
)
$Here = Split-Path -Parent $MyInvocation.MyCommand.Path
$Py = Join-Path $Here "asr_to_vtt.py"
& python $Py --engine whisper --input $InputPath --output-vtt $OutputVtt @Extra
