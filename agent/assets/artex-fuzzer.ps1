<#
.SYNOPSIS
  artex-fuzzer.ps1 — structure-aware file mutation fuzzer for Office-class parsers.
  Runs ON the lab VM. Mutates seed documents, opens them in the target app via COM
  (headless), watches for process death / WER events, groups crashes by signature,
  and keeps a corpus of unique crashing inputs. Driven by ARTEX's fuzz_campaign
  tool (or manually for debugging).

.PARAMETER target   Process: WINWORD / EXCEL
.PARAMETER seeds    Directory with seed files (.docx/.xlsx/.rtf/...)
.PARAMETER out      Working directory (corpus/, crashes/, state.json inside)
.PARAMETER seconds  Campaign duration in seconds (default 300)
.PARAMETER mode     mutation strategy: flip (byte flips), chunk (splice/duplicate
                    structure blocks), xml (structure-aware OOXML part mutation),
                    all (mix; default)
.PARAMETER openTimeout Seconds per open attempt before kill (default 25)

.EXAMPLE
  artex-fuzzer.ps1 -target EXCEL -seeds C:\fuzz\seeds -out C:\fuzz\run1 -seconds 600 -mode all
#>
param(
  [Parameter(Mandatory=$true)][ValidateSet('WINWORD','EXCEL')][string]$Target,
  [Parameter(Mandatory=$true)][string]$Seeds,
  [Parameter(Mandatory=$true)][string]$Out,
  [int]$Seconds = 300,
  [ValidateSet('flip','chunk','xml','all')][string]$Mode = 'all',
  [int]$OpenTimeout = 25
)
$ErrorActionPreference = 'Continue'
$appName = if ($Target -eq 'WINWORD') { 'Word.Application' } else { 'Excel.Application' }
$ext = if ($Target -eq 'WINWORD') { 'docx' } else { 'xlsx' }
New-Item -ItemType Directory -Force -Path "$Out\corpus","$Out\crashes" | Out-Null
$state = [ordered]@{ target=$Target; started=(Get-Date -Format s); mode=$Mode; iterations=0; opens=0; crashes=0; unique=0; killed=0; errors=0 }

$rng = [Random]::new()
function Mutate-Bytes([byte[]]$b, [string]$m) {
  $out = [byte[]]$b.Clone()
  $n = $out.Length
  if ($n -lt 8) { return $out }
  $pick = { param($lo, $hi) $rng.Next([Math]::Max(1,$lo), [Math]::Max(2,$hi)) }
  switch -Regex ($m) {
    'flip' {
      $count = & $pick 1 ([Math]::Min(64, [int]($n/256) + 8))
      for ($i=0; $i -lt $count; $i++) {
        $p = $rng.Next(0, $n)
        $out[$p] = $out[$p] -bxor (1 + $rng.Next(0,7))
      }
    }
    'chunk' {
      # duplicate/cut a block, biased to >64B windows (structure-scale)
      $len = & $pick 16 ([Math]::Min(4096, [int]($n/4) + 16))
      $off = $rng.Next(0, [Math]::Max(1, $n - $len))
      $op = $rng.Next(0,3)
      if ($op -eq 0) { # duplicate
        $block = $out[$off..($off+$len-1)]
        $out = $out[0..($off-1)] + $block + $out[$off..($n-1)]
      } elseif ($op -eq 1) { # cut
        $out = $out[0..($off-1)] + $out[($off+$len)..($n-1)]
      } else { # replace with marker bytes
        for ($i=0; $i -lt $len -and ($off+$i) -lt $n; $i++) { $out[$off+$i] = [byte](0x41 + ($i % 26)) }
      }
    }
    'xml' {
      # structure-aware: target ascii runs that look like tags/attrs/numbers
      $text = [Text.Encoding]::UTF8.GetString($out)
      $count = & $pick 1 24
      for ($i=0; $i -lt $count; $i++) {
        $patterns = @('<w:t[^>]*>', 'val="[^"]*"', '[A-Za-z0-9]{6,}', '\d{4,}', 'r:id="[^"]*"')
        $pat = $patterns | Get-Random
        $ms = [regex]::Matches($text, $pat)
        if ($ms.Count -gt 0) {
          $mm = $ms[$rng.Next(0, $ms.Count)]
          $rep = switch ($rng.Next(0,4)) {
            0 { '2147483647' } 1 { '0' } 2 { ('A' * [Math]::Min(256, $mm.Value.Length * 4)) } 3 { '-1' }
          }
          $text = $text.Substring(0,$mm.Index) + $rep + $text.Substring($mm.Index + $mm.Length)
        }
      }
      $out = [Text.Encoding]::UTF8.GetBytes($text)
    }
  }
  return ,$out
}

# zip-aware mutation: 30% of the time, enter the OOXML zip and mutate a part
function Mutate-Zip([byte[]]$b, [string]$m) {
  $tmpIn = [IO.Path]::GetTempFileName()
  [IO.File]::WriteAllBytes($tmpIn, $b)
  $tmpDir = [IO.Path]::GetTempFileName()
  Remove-Item $tmpIn -ErrorAction SilentlyContinue
  try {
    Add-Type -AssemblyName System.IO.Compression.FileSystem
    $zip = [IO.Compression.ZipFile]::OpenRead($tmpIn)
    $zipDir = "$tmpDir.zip"
    [IO.Compression.ZipFile]::ExtractToDirectory($tmpIn, $zipDir)
    $zip.Dispose()
    $parts = Get-ChildItem $zipDir -Recurse -File | Where-Object { $_.Extension -in '.xml','.rels' }
    if ($parts.Count -gt 0) {
      $part = $parts | Get-Random
      $bytes = [IO.File]::ReadAllBytes($part.FullName)
      $mut = Mutate-Bytes $bytes 'xml'
      [IO.File]::WriteAllBytes($part.FullName, $mut)
      $tmpOut = [IO.Path]::GetTempFileName()
      if (Test-Path $tmpOut) { Remove-Item $tmpOut }
      [IO.Compression.ZipFile]::CreateFromDirectory($zipDir, $tmpOut)
      $rebuilt = [IO.File]::ReadAllBytes($tmpOut)
      Remove-Item $tmpOut
      return ,$rebuilt
    }
    return ,$b
  } catch { return ,$b }
  finally { Remove-Item "$tmpDir*" -Recurse -Force -ErrorAction SilentlyContinue; Remove-Item $tmpIn -ErrorAction SilentlyContinue }
}

# crash signature from WER event for the target process. The event must be
# NEWER than this iteration's open start ($script:caseStart): pre-campaign
# events flushed late by WER otherwise get mis-attributed to the current case
# (task 11's 8 "unique" crashes were exactly this — stale artifacts).
function Get-CrashSignature {
  $events = Get-WinEvent -FilterHashtable @{LogName='Application'; Id=1001; StartTime=$script:campaignStart} -ErrorAction SilentlyContinue |
    Where-Object { $_.Message -match $Target -and $_.TimeCreated -ge $script:caseStart }
  if ($events) {
    $e = $events[-1]
    if ($e.Message -match 'P1: (\S+).*?P2: (\S+).*?P3: (\S+).*?P4: (\S+).*?P5: (\S+).*?P6: (\S+).*?P7: (\S+)' -or
        $e.Message -match 'Application Name: (\S+).*?Application Version: (\S+)') {
      return ($Matches.Values -join '|')
    }
    return ($e.Message.Substring(0, [Math]::Min(200, $e.Message.Length)) -replace '\s+',' ')
  }
  return $null
}

$script:campaignStart = Get-Date
$seedFiles = Get-ChildItem $Seeds -File -ErrorAction SilentlyContinue
if (-not $seedFiles -or $seedFiles.Count -eq 0) { Write-Output (ConvertTo-Json -Compress ([ordered]@{error='no seed files'})); exit 2 }

$deadline = (Get-Date).AddSeconds($Seconds)
$i = 0
while ((Get-Date) -lt $deadline) {
  $i++
  $state.iterations = $i
  $seed = $seedFiles | Get-Random
  try {
    $bytes = [IO.File]::ReadAllBytes($seed.FullName)
    # pick strategy
    $m = if ($Mode -eq 'all') { @('flip','chunk','xml') | Get-Random } else { $Mode }
    # zip-aware for OOXML containers some of the time
    if ($seed.Extension -match 'docx|xlsx|xltx|docm|xlsm' -and $rng.Next(0,100) -lt 30) {
      $mut = Mutate-Zip $bytes $m
    } else {
      $mut = Mutate-Bytes $bytes $m
    }
    $caseFile = "$Out\corpus\case$i." + $seed.Extension
    [IO.File]::WriteAllBytes($caseFile, $mut)

    # open via COM, headless; use a dedicated app instance we kill per iteration
    $app = $null
    $opened = $false
    $script:caseStart = Get-Date   # crash attribution fence (see Get-CrashSignature)
    try {
      $app = New-Object -ComObject $appName
      $app.Visible = $false
      $app.DisplayAlerts = 0
      if ($Target -eq 'WINWORD') {
        $doc = $app.Documents.Open($caseFile, $false, $true) # confirmConversions, readOnly
        $opened = $true
        $doc.Close(0)
      } else {
        $wb = $app.Workbooks.Open($caseFile, 0, $true)
        $opened = $true
        $wb.Close($false)
      }
      $state.opens++
    } catch {
      # COM errors are normal (file rejected); process death is the signal
      if (-not $app) { $state.errors++ } else { $state.opens++ }
    } finally {
      if ($app) {
        try { $app.Quit() } catch {}
        [Runtime.InteropServices.Marshal]::ReleaseComObject($app) | Out-Null
      }
    }
    # Crash detection: WER 1001 event for the target since campaign start is
    # the authoritative signal (process death by our own cleanup is not).
    $sig = Get-CrashSignature
    if ($sig) {
      $state.crashes++
      $sigKey = ($sig -replace '[^A-Za-z0-9|]', '')
      $crashFile = "$Out\crashes\$sigKey.$($seed.Extension)"
      if (-not (Test-Path $crashFile)) {
        Copy-Item $caseFile $crashFile
        $state.unique++
        Add-Content -Path "$Out\crashes\index.txt" -Value "$sig`t$crashFile"
        Write-Output ("UNIQUE_CRASH " + $sig)
      }
    }
    # hard cleanup: any zombie target process left after the iteration
    $procs = Get-Process $Target -ErrorAction SilentlyContinue
    if ($procs) { $procs | Stop-Process -Force }
  } catch { $state.errors++ }
  # persist state each 10 iterations
  if ($i % 10 -eq 0) {
    $state | ConvertTo-Json -Compress | Set-Content "$Out\state.json"
  }
}
$state.finished = (Get-Date -Format s)
$state | ConvertTo-Json -Compress | Set-Content "$Out\state.json"
Write-Output ($state | ConvertTo-Json -Compress)
