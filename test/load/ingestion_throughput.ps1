[CmdletBinding()]
param(
    [string]$WorkersList = '1,2,4,8',
    [string]$Books = '10,11,16,23,35,41,42,43,45,46,55,74,76,84,98,100,120,145,161,174,204,205,209,244,345,394,408,768,844,863,1080,1184,1232,1259,1260,1342,1400,1497,1513,1661,1695,1727,1952,1998,2148,2160,2465,2542,2554,2591,2600,2641,2680,2701,2852,3207,3268,3296,4085,4300,4363,5197,5200,6593,6761,7370,8492,12342,15399,16328,16389,20203,25344,26471,28054,34901,37106,37683,49008,49297,57669,58820,58866,59828,59892,61168,61262,61851,62215,62383,63469,64317,65238,67979,68283,69087,78004,78005,78009,78010',
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
        $samplerScript = (Join-Path $PSScriptRoot 'sample_resources.ps1')
        $sampler = Start-Process powershell -PassThru -ArgumentList @(
            '-NoProfile', '-File', "`"$samplerScript`"",
            '-ProcessId', "$($svc.Id)",
            '-OutFile', "`"$csv`""
        )
        Write-Host "sampler PID $($sampler.Id) -> $csv"
    }

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

    Remove-Item -Recurse -Force $datalake -ErrorAction SilentlyContinue
}

Write-Host ""
Write-Host '=== SUMMARY (cold datalake, 2xx-only throughput) ==='
$summaryRows | Format-Table -AutoSize

$summaryCsv = Join-Path $OutDir "ingestion_throughput_summary_$stamp.csv"
$summaryRows | Export-Csv -NoTypeInformation -Path $summaryCsv -Encoding UTF8
Write-Host "Summary CSV: $summaryCsv"
