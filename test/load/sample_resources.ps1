<#
.SYNOPSIS
  Samples the CPU and memory of a running Boogle service (default: the Indexing Service) while a
  load test drives it, and writes one CSV row per interval.

.DESCRIPTION
  For every interval the script records the process's CPU usage (computed from the delta of its
  TotalProcessorTime, so it is a real percentage of one core scaled to the whole machine), the
  working set, the private memory and the thread count. Optionally it also records the CPU and
  memory of a Docker container (MongoDB) through `docker stats`.

  The CSV keeps the real elapsed time of every sample, so gaps introduced by slow docker calls do
  not distort the CPU calculation. Run it in a second terminal while k6 runs the benchmark; the
  process is found by name (the newest matching process), which works for a binary built with
  `go build -o bin/indexing.exe ./cmd/indexing`.

.PARAMETER ProcessName
  Base name of the process to sample, without .exe. Default: indexing.

.PARAMETER ProcessId
  Sample this exact process ID instead of searching by name.

.PARAMETER IntervalMs
  Milliseconds between samples. Default: 1000.

.PARAMETER DurationS
  Total sampling time in seconds; 0 (default) samples until Ctrl+C.

.PARAMETER OutFile
  CSV file to write (header included). Default: docs/evidence/indexing_resources.csv. The folder
  must exist.

.PARAMETER DockerContainer
  Optional Docker container to sample alongside the process (e.g. boogle-mongo). Empty (default)
  skips docker stats.

.EXAMPLE
  powershell -File test/load/sample_resources.ps1 -DurationS 120 -DockerContainer boogle-mongo
#>
[CmdletBinding()]
param(
    [string]$ProcessName = 'indexing',
    [int]$ProcessId = 0,
    [int]$IntervalMs = 1000,
    [int]$DurationS = 0,
    [string]$OutFile = 'docs/evidence/indexing_resources.csv',
    [string]$DockerContainer = ''
)

$ErrorActionPreference = 'Stop'
$processorCount = [Environment]::ProcessorCount

function Resolve-TargetProcess {
    if ($ProcessId -gt 0) {
        return Get-Process -Id $ProcessId -ErrorAction Stop
    }
    $candidates = @(Get-Process -Name $ProcessName -ErrorAction SilentlyContinue | Sort-Object StartTime -Descending)
    if ($candidates.Count -eq 0) {
        throw "No running process named '$ProcessName'. Start the service (go build -o bin/$ProcessName.exe ...) first, or pass -Pid."
    }
    return $candidates[0]
}

$target = Resolve-TargetProcess
Write-Host "sampling PID $($target.Id) ($($target.ProcessName)) every ${IntervalMs}ms -> $OutFile"
if ($DockerContainer) {
    Write-Host "sampling docker container '$DockerContainer' on every tick"
}

$header = 'Timestamp,ElapsedS,PID,CPUPercent,WorkingSetMB,PrivateMB,Threads,MongoCPUPercent,MongoMemUsage'
Set-Content -LiteralPath $OutFile -Value $header -Encoding UTF8

$start = Get-Date
$previousCpuMs = $target.TotalProcessorTime.TotalMilliseconds
$previousAt = $start
$samples = 0

while ($true) {
    Start-Sleep -Milliseconds $IntervalMs

    try {
        $target.Refresh()
    } catch {
        Write-Host "process exited after $samples samples"
        break
    }

    $now = Get-Date
    $currentCpuMs = $target.TotalProcessorTime.TotalMilliseconds
    $wallMs = ($now - $previousAt).TotalMilliseconds
    if ($wallMs -gt 0) {
        $cpuPercent = (($currentCpuMs - $previousCpuMs) / $wallMs) * 100 / $processorCount
    } else {
        $cpuPercent = 0
    }
    $previousCpuMs = $currentCpuMs
    $previousAt = $now

    $mongoCpu = ''
    $mongoMem = ''
    if ($DockerContainer) {
        try {
            $stats = docker stats $DockerContainer --no-stream --format '{{.CPUPerc}};{{.MemUsage}}' 2>$null
            if ($stats) {
                $parts = $stats.ToString().Split(';')
                $mongoCpu = $parts[0].TrimEnd('%')
                $mongoMem = if ($parts.Count -gt 1) { $parts[1].Trim() } else { '' }
            }
        } catch {
            # Docker unreachable: keep the columns empty and keep sampling the process.
        }
    }

    $elapsed = ($now - $start).TotalSeconds
    # InvariantCulture keeps decimals as dots, so the CSV splits cleanly with any locale.
    $row = '{0},{1},{2},{3},{4},{5},{6},{7},{8}' -f `
        $now.ToString('yyyy-MM-ddTHH:mm:ss.fff', [cultureinfo]::InvariantCulture), `
        $elapsed.ToString('F1', [cultureinfo]::InvariantCulture), `
        $target.Id, `
        $cpuPercent.ToString('F1', [cultureinfo]::InvariantCulture), `
        ($target.WorkingSet64 / 1MB).ToString('F2', [cultureinfo]::InvariantCulture), `
        ($target.PrivateMemorySize64 / 1MB).ToString('F2', [cultureinfo]::InvariantCulture), `
        @($target.Threads).Count, $mongoCpu, $mongoMem
    Add-Content -LiteralPath $OutFile -Value $row
    $samples++

    if ($samples % 10 -eq 0) {
        Write-Host ("t={0:F0}s cpu={1:F1}% ws={2:F0}MB samples={3}" -f $elapsed, $cpuPercent, ($target.WorkingSet64 / 1MB), $samples)
    }
    if ($DurationS -gt 0 -and $elapsed -ge $DurationS) {
        break
    }
}

Write-Host "wrote $samples samples to $OutFile"
