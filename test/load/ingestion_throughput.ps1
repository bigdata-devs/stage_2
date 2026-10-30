<#
.SYNOPSIS
  Measures Ingestion Service throughput (books/s) on a COLD datalake at several
  -workers settings, capturing CPU and RAM samples for every run.

.DESCRIPTION
  For each value in -WorkersList (default 1, 2, 4 and 8, per the Stage 2 audit)
  the script:
    1. builds the ingestion binary and the HTTP benchmark driver,
    2. starts a fresh ingestion service with an empty temporary datalake,
    3. starts test/load/sample_resources.ps1 in the background to sample the
       process's CPU %, working set and private memory,
    4. drives the service with test/load/ingestion_benchmark.go over ~100 real
       Project Gutenberg books, counting only HTTP 2xx answers as ingested,
    5. stops the service and the sampler, keeping the CSV and the throughput
       transcript per workers setting under -OutDir.

  Run it from the repository root. It needs network access to
  https://www.gutenberg.org. Because every run starts from an empty datalake,
  every book is downloaded fresh (cold start), which is what the audit asks for.

.PARAMETER WorkersList
  Comma-separated server-side -workers values to benchmark. Default: 1,2,4,8.

.PARAMETER Books
  Comma-separated Gutenberg book IDs. Default: the ~45 IDs exercised by this
  repository's own e2e tests and benchmarks. Pass a longer list (aim for ~100)
  for the full audit run, e.g. by appending more IDs from
  https://www.gutenberg.org/ebooks/.

.PARAMETER Port
  Host port for the temporary ingestion service. Default: 7016 (isolated from
  the compose stack on 7001).

.PARAMETER OutDir
  Folder where per-run CSVs and throughput transcripts are written.
  Default: test/load/results (create it first or let the script create it).

.PARAMETER RequestConcurrency
  Max concurrent POST /ingest requests driven by the benchmark. 0 (default)
  picks max(workers x 2, 10) so the client never starves the server slots.

.PARAMETER SkipResources
  Skip the CPU/RAM sampler (useful for quick smoke runs).

.EXAMPLE
  powershell -File test/load/ingestion_throughput.ps1

.EXAMPLE
  powershell -File test/load/ingestion_throughput.ps1 -WorkersList 1,8 -SkipResources
#>
[CmdletBinding()]
param(
    [string]$WorkersList = '1,2,4,8',
    [string]$Books = '5,11,26,43,46,55,74,76,84,98,111,120,174,190,201,205,345,408,500,521,844,900,1010,1080,1232,1260,1342,1355,1400,1661,1952,2500,2701,4300,17489,158,215,219,236,242,253,260,276,360,434,514,574,600,613,718,1184,1322,1497,996,1001,2554,2600,2805,2852',
    [int]$Port = 7016,
    [string]$OutDir = 'test/load/results',
    [int]$RequestConcurrency = 0,
    [switch]$SkipResources
)

$ErrorActionPreference = 'Stop'
$repoRoot = Resolve-Path (Join-Path $PSScriptRoot '..\..')
Set-Location $repoRoot

$workersValues = @($WorkersList.Split(',') | ForEach-Object { $_.Trim() } | Where-Object { $_ })
if ($workersValues.Count -eq 0) { throw 'WorkersList is empty.' }
$bookIds = @($Books.Split(',') | ForEach-Object { $_.Trim() } | Where-Object { $_ })
if ($bookIds.Count -eq 0) { throw 'Books is empty.' }

New-Item -ItemType Directory -Force -Path $OutDir | Out-Null
$stamp = Get-Date -Format 'yyyy-MM-ddTHH-mm-ssZ'
$summaryRows = @()

Write-Host "=== Ingestion throughput benchmark (cold datalake) ==="
Write-Host "Workers list : $($workersValues -join ', ')"
Write-Host "Books        : $($bookIds.Count) (2xx-only counting)"
Write-Host "Port         : $Port"
Write-Host "Out dir      : $OutDir"
Write-Host ""

Write-Host '== building binaries =='
$benchBin = Join-Path $env:TEMP "ingestion_benchmark_$stamp.exe"
$ingBin   = Join-Path $env:TEMP "ingestion_svc_$stamp.exe"
go build -o $benchBin ./test/load/ingestion_benchmark.go
if ($LASTEXITCODE -ne 0) { throw 'failed to build ingestion_benchmark.go' }
go build -o $ingBin ./cmd/ingestion
if ($LASTEXITCODE -ne 0) { throw 'failed to build cmd/ingestion' }
Write-Host 'PASS: binaries built'

$booksArg = $bookIds -join ','

foreach ($w in $workersValues) {
    Write-Host ""
    Write-Host "== workers=$w : starting cold ingestion on :$Port =="

    $datalake = Join-Path $env:TEMP "ingestion_cold_w${w}_$stamp"
    if (Test-Path $datalake) { Remove-Item -Recurse -Force $datalake }
    New-Item -ItemType Directory -Force -Path $datalake | Out-Null

    $svcLog  = Join-Path $OutDir "ingestion_w${w}_$stamp.log"
    $svc = Start-Process -FilePath $ingBin `
        -ArgumentList '-addr', ":$Port", '-datalake', $datalake, '-workers', "$w" `
        -PassThru -RedirectStandardOutput $svcLog -RedirectStandardError "$svcLog.err"
    Write-Host "service PID $($svc.Id), log $svcLog"

    $sampler = $null
    if (-not $SkipResources) {
        $csv = Join-Path $OutDir "ingestion_resources_w${w}_$stamp.csv"
        $sampler = Start-Process powershell -PassThru -ArgumentList @(
            '-NoProfile', '-File', (Join-Path $PSScriptRoot 'sample_resources.ps1'),
            '-ProcessId', "$($svc.Id)",
            '-OutFile', $csv
        )
        Write-Host "sampler PID $($sampler.Id) -> $csv"
    }

    # Wait for readiness on /ingest/list
    $ready = $false
    for ($i = 0; $i -lt 60; $i++) {
        try {
            $r = Invoke-WebRequest -Uri "http://localhost:$Port/ingest/list" -UseBasicParsing -TimeoutSec 2
            if ($r.StatusCode -eq 200) { $ready = $true; break }
        } catch { }
        Start-Sleep -Milliseconds 500
    }
    if (-not $ready) {
        Stop-Process -Id $svc.Id -Force -ErrorAction SilentlyContinue
        if ($sampler) { Stop-Process -Id $sampler.Id -Force -ErrorAction SilentlyContinue }
        throw "ingestion (workers=$w) never answered /ingest/list on :$Port"
    }
    Write-Host 'service ready'

    $conc = if ($RequestConcurrency -gt 0) { $RequestConcurrency } else { [Math]::Max($w * 2, 10) }
    $throughputOut = Join-Path $OutDir "ingestion_throughput_w${w}_$stamp.txt"

    & $benchBin -url "http://localhost:$Port" -books $booksArg -concurrency $conc -out $throughputOut
    if ($LASTEXITCODE -ne 0) { Write-Warning "benchmark exited with code $LASTEXITCODE for workers=$w" }

    Stop-Process -Id $svc.Id -Force -ErrorAction SilentlyContinue
    if ($sampler) { Stop-Process -Id $sampler.Id -Force -ErrorAction SilentlyContinue }
    Start-Sleep -Milliseconds 500

    # Parse the transcript for the summary row
    $text = Get-Content $throughputOut -Raw
    $ingested  = if ($text -match 'Ingested \(2xx\):\s+(\d+)') { [int]$Matches[1] } else { 0 }
    $failed    = if ($text -match 'Failed \(non-2xx\):\s+(\d+)') { [int]$Matches[1] } else { 0 }
    $elapsed   = if ($text -match 'Total elapsed time:\s+([\d.]+)') { [double]$Matches[1] } else { 0 }
    $thput     = if ($text -match 'Ingestion throughput:\s+([\d.]+)') { [double]$Matches[1] } else { 0 }

    $cpuAvg = ''; $wsMax = ''
    if (-not $SkipResources) {
        $csvPath = Join-Path $OutDir "ingestion_resources_w${w}_$stamp.csv"
        if (Test-Path $csvPath) {
            $samples = Import-Csv $csvPath
            if ($samples.Count -gt 0) {
                $cpuAvg = ($samples | Measure-Object -Property CPUPercent -Average).Average
                $wsMax  = ($samples | Measure-Object -Property WorkingSetMB -Maximum).Maximum
            }
        }
    }

    $summaryRows += [pscustomobject]@{
        Workers  = $w
        Ingested = $ingested
        Failed   = $failed
        ElapsedS = $elapsed
        BooksPerS = $thput
        CPUAvgPct = if ($cpuAvg -ne '') { [Math]::Round($cpuAvg, 1) } else { '' }
        WSMaxMB   = if ($wsMax -ne '') { [Math]::Round($wsMax, 0) } else { '' }
    }
    Write-Host ("workers={0} ingested={1} failed={2} elapsed={3}s books/s={4}" -f $w, $ingested, $failed, $elapsed, $thput)

    # Clean the cold datalake so the next workers value starts cold too
    Remove-Item -Recurse -Force $datalake -ErrorAction SilentlyContinue
}

Write-Host ""
Write-Host '=== SUMMARY (cold datalake, 2xx-only throughput) ==='
$summaryRows | Format-Table -AutoSize

$summaryCsv = Join-Path $OutDir "ingestion_throughput_summary_$stamp.csv"
$summaryRows | Export-Csv -NoTypeInformation -Path $summaryCsv -Encoding UTF8
Write-Host "Summary CSV: $summaryCsv"
Write-Host 'NOTE: run this on a machine with stable network to gutenberg.org;'
Write-Host '      extend -Books toward ~100 real IDs for the full audit evidence.'
