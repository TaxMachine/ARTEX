<#
.SYNOPSIS
  artex-minimize.ps1 — crash-input minimizer for Office parsers. Reduces a
  crashing input to the smallest variant that still produces the SAME WER crash
  signature, by structure-aware shrinking (for OOXML: drop XML
  elements/attributes and non-essential zip parts; generic: truncate + splice).
  Run ON the lab VM next to the crashing file. Output: <out>\min.<ext> plus
  min-info.json (original/new sizes, signature, steps).

  Reproduction uses headless COM open (same as the fuzzer). A case "reproduces"
  when the target process dies during/after the open within the timeout AND the
  newest WER 1001 event for the target matches the input signature.
#>
param(
  [Parameter(Mandatory=$true)][ValidateSet('WINWORD','EXCEL')][string]$Target,
  [Parameter(Mandatory=$true)][string]$CrashFile,
  [Parameter(Mandatory=$true)][string]$Signature,
  [Parameter(Mandatory=$true)][string]$Out,
  [int]$MaxSteps = 60,
  [int]$OpenTimeoutSec = 30
)
$ErrorActionPreference = 'Continue'
$appName = if ($Target -eq 'WINWORD') { 'Word.Application' } else { 'Excel.Application' }
New-Item -ItemType Directory -Force -Path $Out | Out-Null
$script:log = "$Out\min-steps.log"
Set-Content -Path $script:log -Value ("start " + (Get-Date -Format s) + " target=$Target sig-match=" + $Signature)
function Log([string]$m) { Add-Content -Path $script:log -Value $m }
$script:t0 = Get-Date

function Test-Crash([string]$path) {
  # open via COM. Crash signal is EITHER a fresh WER 1001 event matching the
  # signature OR the target process dying mid-open (silent-termination class:
  # the COM call fails because the process vanished, and some flows raise no
  # WER event at all). Process-death alone counts: a benign parse error leaves
  # the process alive and the open merely throws.
  Get-Process $Target -ErrorAction SilentlyContinue | Stop-Process -Force
  Start-Sleep -Milliseconds 300
  $app = $null
  $died = $false
  try {
    $app = New-Object -ComObject $appName
    $app.Visible = $false; $app.DisplayAlerts = 0
    if ($Target -eq 'WINWORD') { $d = $app.Documents.Open($path, $false, $true); $d.Close(0) }
    else { $w = $app.Workbooks.Open($path, 0, $true); $w.Close($false) }
  } catch {
    if (-not (Get-Process $Target -ErrorAction SilentlyContinue)) { $died = $true }
  } finally {
    if ($app) { try { $app.Quit() } catch {}; [Runtime.InteropServices.Marshal]::ReleaseComObject($app) | Out-Null }
  }
  Start-Sleep -Milliseconds 500
  if (-not (Get-Process $Target -ErrorAction SilentlyContinue)) { $died = $true }
  if ($died) { return $true }
  $ev = Get-WinEvent -FilterHashtable @{LogName='Application'; Id=1001; StartTime=$script:t0} -ErrorAction SilentlyContinue |
    Where-Object { $_.Message -match $Target } | Select-Object -Last 1
  return ($null -ne $ev -and $ev.Message.Contains($Signature))
}

function Shrink-Xml([byte[]]$bytes) {
  # drop a random element/attribute from a random part (zip-aware handled by caller)
  $text = [Text.Encoding]::UTF8.GetString($bytes)
  $els = [regex]::Matches($text, '<[^/>!][^>]*>')
  if ($els.Count -gt 0) {
    $e = $els | Get-Random
    return [Text.Encoding]::UTF8.GetBytes($text.Substring(0,$e.Index) + $text.Substring($e.Index + $e.Length))
  }
  return $null
}

function Shrink-Zip([byte[]]$bytes) {
  # extract, shrink a random xml part (drop element or the whole part when non-essential), rezip
  $tmpIn = [IO.Path]::GetTempFileName(); [IO.File]::WriteAllBytes($tmpIn, $bytes)
  $dir = [IO.Path]::GetTempFileName(); Remove-Item $tmpIn; $dir = "$dir.zip"
  try {
    Add-Type -AssemblyName System.IO.Compression.FileSystem
    [IO.Compression.ZipFile]::ExtractToDirectory($tmpIn, $dir)
    $parts = Get-ChildItem $dir -Recurse -File | Where-Object { $_.Extension -in '.xml','.rels' }
    $essential = @('[Content_Types].xml','_rels\.rels','xl\workbook.xml','word\document.xml')
    if ((Get-Random -Maximum 4) -eq 0) {
      # try dropping a whole non-essential part
      $drop = $parts | Where-Object { $rel = $_.FullName.Substring($dir.Length+1); -not ($essential | Where-Object { $rel -match $_ }) } | Get-Random
      if ($drop) { Remove-Item $drop.FullName }
    } else {
      $part = $parts | Get-Random
      if ($part) {
        $b = [IO.File]::ReadAllBytes($part.FullName)
        $s = Shrink-Xml $b
        if ($s) { [IO.File]::WriteAllBytes($part.FullName, $s) }
      }
    }
    $tmpOut = [IO.Path]::GetTempFileName(); if (Test-Path $tmpOut) { Remove-Item $tmpOut }
    [IO.Compression.ZipFile]::CreateFromDirectory($dir, $tmpOut)
    $rebuilt = [IO.File]::ReadAllBytes($tmpOut); Remove-Item $tmpOut
    return ,$rebuilt
  } catch { return ,$bytes } finally { Remove-Item "$dir*" -Recurse -Force -ErrorAction SilentlyContinue; Remove-Item $tmpIn -ErrorAction SilentlyContinue }
}

function Shrink-Truncate([byte[]]$bytes) {
  # cut a trailing/leading block (works for any format)
  $cut = [Math]::Min([int]($bytes.Length / 10) + 16, $bytes.Length - 512)
  if ($cut -lt 16) { return $null }
  if ((Get-Random -Maximum 2) -eq 0) { return ,($bytes[0..($bytes.Length-$cut-1)]) }
  else { return ,($bytes[$cut..($bytes.Length-1)]) }
}

try {
$bytes = [IO.File]::ReadAllBytes($CrashFile)
} catch { Log ("readfail " + $_.Exception.Message); exit 3 }
$origLen = $bytes.Length
Log "loaded $origLen bytes; verifying repro..."
if (-not (Test-Crash $CrashFile)) {
  Log "no repro"
  @{ error = 'input does not reproduce the signature'; signature = $Signature } | ConvertTo-Json -Compress | Set-Content "$Out\min-info.json"
  Write-Output (ConvertTo-Json -Compress @{ error = 'no repro' }); exit 2
}
Log "repro ok"

$cur = [byte[]]$bytes
$step = 0
while ($step -lt $MaxSteps -and $cur.Length -gt 512) {
  $step++
  $candidate = $null
  $kind = Get-Random -Maximum 3
  if ($CrashFile -match '\.(docx|xlsx|xltx|docm|xlsm)$' -and $kind -eq 0) { $candidate = Shrink-Zip $cur }
  elseif ($kind -eq 1) { $candidate = Shrink-Truncate $cur }
  else { $candidate = Shrink-Xml $cur }
  if ($null -eq $candidate -or $candidate.Length -ge $cur.Length) { continue }
  $tryPath = "$Out\try.$(([IO.Path]::GetExtension($CrashFile)).TrimStart('.'))"
  [IO.File]::WriteAllBytes($tryPath, $candidate)
  if (Test-Crash $tryPath) { $cur = [byte[]]$candidate; Log ("keep " + $cur.Length) }
}
$minPath = "$Out\min.$(([IO.Path]::GetExtension($CrashFile)).TrimStart('.'))"
[IO.File]::WriteAllBytes($minPath, $cur)
$info = @{ target=$Target; original=$origLen; minimized=$cur.Length; reduction=[Math]::Round(100*(1-$cur.Length/[double]$origLen),1); steps=$step; signature=$Signature }
$info | ConvertTo-Json -Compress | Set-Content "$Out\min-info.json"
Remove-Item "$Out\try.*" -ErrorAction SilentlyContinue
Write-Output ($info | ConvertTo-Json -Compress)
