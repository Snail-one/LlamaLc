param(
    [string]$ModelsDir = "models",
    [string]$MmprojDir = "mmproj"
)

$ErrorActionPreference = "Stop"
$ScriptDir = Split-Path -Parent $MyInvocation.MyCommand.Path
$Root = $ScriptDir
if (-not (Test-Path (Join-Path $Root "llama-server.exe"))) {
    $ParentDir = Split-Path -Parent $ScriptDir
    if (Test-Path (Join-Path $ParentDir "llama-server.exe")) {
        $Root = $ParentDir
    }
}
Set-Location $Root

function Format-Size {
    param([long]$Bytes)

    if ($Bytes -ge 1GB) { return "{0:N2} GB" -f ($Bytes / 1GB) }
    if ($Bytes -ge 1MB) { return "{0:N2} MB" -f ($Bytes / 1MB) }
    return "$Bytes B"
}

function Read-Default {
    param(
        [string]$Prompt,
        [string]$Default
    )

    $value = Read-Host "$Prompt [$Default]"
    if ([string]::IsNullOrWhiteSpace($value)) { return $Default }
    return $value.Trim()
}

function Read-MenuChoice {
    param(
        [string]$Prompt,
        [int]$Default,
        [int]$Min,
        [int]$Max
    )

    do {
        $choice = Read-Default $Prompt ([string]$Default)
        $index = 0
        $isNumber = [int]::TryParse($choice, [ref]$index)
    } until ($isNumber -and $index -ge $Min -and $index -le $Max)

    return $index
}

function Split-CommandLine {
    param([string]$Text)

    if ([string]::IsNullOrWhiteSpace($Text)) { return @() }

    $argMatches = [regex]::Matches($Text, '("[^"]*"|''[^'']*''|\S+)')
    $items = @()
    foreach ($argMatch in $argMatches) {
        $item = $argMatch.Value
        if (($item.StartsWith('"') -and $item.EndsWith('"')) -or ($item.StartsWith("'") -and $item.EndsWith("'"))) {
            $item = $item.Substring(1, $item.Length - 2)
        }
        $items += $item
    }
    return $items
}

function Resolve-InputPath {
    param([string]$Path)

    if ([string]::IsNullOrWhiteSpace($Path)) { return "" }

    $cleanPath = $Path.Trim().Trim('"').Trim("'")
    if (-not [System.IO.Path]::IsPathRooted($cleanPath)) {
        $cleanPath = Join-Path $Root $cleanPath
    }

    return [System.IO.Path]::GetFullPath($cleanPath)
}

function Select-Mmproj {
    param(
        [string]$Directory,
        [string]$ModelName = "",
        [System.IO.FileInfo]$RecommendedMmproj = $null
    )

    if (-not (Test-Path $Directory)) {
        return ""
    }

    $mmprojFiles = @(Get-ChildItem -Path $Directory -Recurse -File -Include *.gguf | Sort-Object Name)
    if ($mmprojFiles.Count -eq 0) {
        return ""
    }

    $defaultIndex = 0
    if ($null -ne $RecommendedMmproj) {
        for ($i = 0; $i -lt $mmprojFiles.Count; $i++) {
            if ($mmprojFiles[$i].FullName.Equals($RecommendedMmproj.FullName, [System.StringComparison]::OrdinalIgnoreCase)) {
                $defaultIndex = $i + 1
                break
            }
        }
    }

    Write-Host ""
    if (-not [string]::IsNullOrWhiteSpace($ModelName)) {
        Write-Host "当前选择模型: $ModelName" -ForegroundColor Cyan
    }
    if ($defaultIndex -gt 0) {
        Write-Host "自动匹配 mmproj: $($mmprojFiles[$defaultIndex - 1].Name)" -ForegroundColor Cyan
    }
    Write-Host "发现 mmproj 多模态投影器:"
    Write-Host "   0. 不使用 mmproj"
    for ($i = 0; $i -lt $mmprojFiles.Count; $i++) {
        $size = Format-Size $mmprojFiles[$i].Length
        $label = ""
        if (($i + 1) -eq $defaultIndex) {
            $label = "  [推荐]"
        }
        Write-Host ("  {0,2}. {1}  ({2}){3}" -f ($i + 1), $mmprojFiles[$i].Name, $size, $label)
    }

    $index = Read-MenuChoice -Prompt "请选择 mmproj 编号" -Default $defaultIndex -Min 0 -Max $mmprojFiles.Count

    if ($index -eq 0) {
        return ""
    }

    return $mmprojFiles[$index - 1].FullName
}

function Get-ModelId {
    param([System.IO.FileInfo]$ModelFile)

    return $ModelFile.Name
}

function Get-MmprojMatchKey {
    param([string]$FileName)

    $baseName = [System.IO.Path]::GetFileNameWithoutExtension($FileName)
    $parts = $baseName -split '[-_]', 2
    if ($parts.Count -eq 0) { return "" }
    return $parts[0]
}

function Find-MatchingMmproj {
    param(
        [System.IO.FileInfo]$ModelFile,
        [System.IO.FileInfo[]]$MmprojFiles
    )

    if ($MmprojFiles.Count -eq 0) { return $null }

    $modelKey = Get-MmprojMatchKey $ModelFile.Name
    if ([string]::IsNullOrWhiteSpace($modelKey)) { return $null }

    $matches = @($MmprojFiles | Where-Object {
        $mmprojKey = Get-MmprojMatchKey $_.Name
        -not [string]::IsNullOrWhiteSpace($mmprojKey) -and
            $mmprojKey.Equals($modelKey, [System.StringComparison]::OrdinalIgnoreCase)
    })

    if ($matches.Count -eq 0) { return $null }
    return $matches[0]
}

function Find-RecommendedModelIndex {
    param(
        [System.IO.FileInfo[]]$ModelFiles,
        [System.IO.FileInfo[]]$MmprojFiles
    )

    if ($ModelFiles.Count -eq 0 -or $MmprojFiles.Count -eq 0) { return 1 }

    for ($i = 0; $i -lt $ModelFiles.Count; $i++) {
        $matchedMmproj = Find-MatchingMmproj -ModelFile $ModelFiles[$i] -MmprojFiles $MmprojFiles
        if ($null -ne $matchedMmproj) {
            return $i + 1
        }
    }

    return 1
}

function Write-RouterPreset {
    param(
        [System.IO.FileInfo[]]$ModelFiles,
        [string]$OutputPath
    )

    $lines = @()
    $lines += "; Generated by llama-launcher.ps1"
    $lines += "; API model ids use GGUF filenames."
    $lines += "version = 1"
    $lines += ""

    foreach ($modelFile in $ModelFiles) {
        $modelId = Get-ModelId $modelFile
        $lines += "[$modelId]"
        $lines += "model = $($modelFile.FullName)"
        $lines += "n-gpu-layers = auto"
        $lines += "load-on-startup = false"

        $lines += ""
    }

    Set-Content -Path $OutputPath -Value $lines -Encoding ASCII
}

function Write-ManualRouterPreset {
    param(
        [System.IO.FileInfo[]]$ModelFiles,
        [System.IO.FileInfo[]]$MmprojFiles,
        [string]$OutputPath,
        [bool]$AutoMatchMmproj = $true
    )

    $lines = @()
    $lines += "; Generated by llama-launcher.ps1"
    $lines += "; Manual router preset. Edit this file as needed; launcher will not overwrite it unless you choose menu option 4."
    $lines += "; API model ids use GGUF filenames."
    $lines += "; mmproj auto-match by filename prefix: $AutoMatchMmproj"
    $lines += ";"
    $lines += "; Common per-model options:"
    $lines += ";   model             = main GGUF model path"
    $lines += ";   mmproj            = multimodal projector GGUF path, uncomment for vision models"
    $lines += ";   image-min-tokens  = minimum image tokens, common value: 1024"
    $lines += ";   n-gpu-layers      = auto, all, 0, or a number"
    $lines += ";   ctx-size          = context length, for example 4096, 8192, 32768"
    $lines += ";   load-on-startup   = true or false"
    $lines += ";   threads           = CPU threads, uncomment if you want to override auto"
    $lines += ""

    if ($MmprojFiles.Count -gt 0) {
        $lines += "; Available mmproj files:"
        foreach ($mmprojFile in $MmprojFiles) {
            $lines += ";   $($mmprojFile.FullName)"
        }
        $lines += ""
    }

    foreach ($modelFile in $ModelFiles) {
        $modelId = Get-ModelId $modelFile
        $matchedMmproj = $null
        if ($AutoMatchMmproj) {
            $matchedMmproj = Find-MatchingMmproj -ModelFile $modelFile -MmprojFiles $MmprojFiles
        }
        $lines += "[$modelId]"
        $lines += "model = $($modelFile.FullName)"
        $lines += "n-gpu-layers = auto"
        $lines += "load-on-startup = false"
        $lines += "; ctx-size = 8192"
        $lines += "; threads = 20"
        if ($null -ne $matchedMmproj) {
            $lines += "mmproj = $($matchedMmproj.FullName)"
            $lines += "image-min-tokens = 1024"
        } elseif ($MmprojFiles.Count -gt 0) {
            $lines += "; mmproj = $($MmprojFiles[0].FullName)"
            $lines += "; image-min-tokens = 1024"
        } else {
            $lines += "; mmproj = E:\path\to\mmproj.gguf"
            $lines += "; image-min-tokens = 1024"
        }
        $lines += ""
    }

    Set-Content -Path $OutputPath -Value $lines -Encoding ASCII
}

function Format-DisplayArg {
    param([string]$Value)

    $display = $Value
    if ($Value.StartsWith($Root, [System.StringComparison]::OrdinalIgnoreCase)) {
        $relative = $Value.Substring($Root.Length).TrimStart('\', '/')
        $display = ".\$relative"
    }

    if ($display -match '\s') {
        return '"' + ($display -replace '"', '\"') + '"'
    }
    return $display
}

function Get-DisplayCommand {
    param(
        [string]$Exe,
        [string[]]$ArgumentList
    )

    $displayExe = Format-DisplayArg $Exe
    $escapedArgs = $ArgumentList | ForEach-Object { Format-DisplayArg $_ }
    return "$displayExe $($escapedArgs -join ' ')"
}

function Show-FinalConfirm {
    param(
        [string]$Exe,
        [string[]]$ArgumentList,
        [string]$Url = ""
    )

    $command = Get-DisplayCommand -Exe $Exe -ArgumentList $ArgumentList

    Write-Host ""
    Write-Host "最终运行参数确认:" -ForegroundColor Cyan
    Write-Host "  $command" -ForegroundColor DarkCyan
    if (-not [string]::IsNullOrWhiteSpace($Url)) {
        Write-Host "本地访问地址: $Url" -ForegroundColor Green
    }
    Write-Host ""

    $answer = Read-Host "确认使用以上参数启动？Y/N [Y]"
    if ([string]::IsNullOrWhiteSpace($answer)) { return $true }
    return $answer -match '^[Yy]'
}

$serverExe = Join-Path $Root "llama-server.exe"
$cliExe = Join-Path $Root "llama-cli.exe"
$fallbackCliExe = Join-Path $Root "llama.exe"

if (-not (Test-Path $serverExe)) {
    Write-Host "找不到 llama-server.exe，请确认脚本放在 llama.cpp 可执行文件目录。" -ForegroundColor Red
    Read-Host "按 Enter 退出"
    exit 1
}

if (-not (Test-Path $cliExe) -and (Test-Path $fallbackCliExe)) {
    $cliExe = $fallbackCliExe
}

$modelRoot = Join-Path $Root $ModelsDir
$mmprojRoot = Join-Path $Root $MmprojDir
if (-not (Test-Path $modelRoot)) {
    New-Item -ItemType Directory -Path $modelRoot | Out-Null
    Write-Host "已创建 models 目录：$modelRoot" -ForegroundColor Yellow
}
if (-not (Test-Path $mmprojRoot)) {
    New-Item -ItemType Directory -Path $mmprojRoot | Out-Null
    Write-Host "已创建 mmproj 目录：$mmprojRoot" -ForegroundColor Yellow
}

$models = Get-ChildItem -Path $modelRoot -Recurse -File -Include *.gguf,*.bin,*.ggml | Sort-Object Name
if ($models.Count -eq 0) {
    Write-Host "models 目录下没有找到模型文件（支持 .gguf / .bin / .ggml）。" -ForegroundColor Red
    Write-Host "请把模型放到：$modelRoot"
    Read-Host "按 Enter 退出"
    exit 1
}

Clear-Host
Write-Host "llama.cpp 一键启动器" -ForegroundColor Green
Write-Host "当前目录: $Root"
Write-Host ""
Write-Host "运行方式:"
Write-Host "  1. 单模型 API 服务（llama-server，默认推荐）"
Write-Host "  2. 创建/刷新手动路由配置（router-models.ini）"
Write-Host "  3. API 多模型路由（GET /models 可查看全部模型）"
Write-Host "  4. 命令行聊天（llama-cli）"
Write-Host ""
$mode = [string](Read-MenuChoice -Prompt "请选择运行方式" -Default 1 -Min 1 -Max 4)

if ($mode -eq "2") {
    $routerModels = @($models | Where-Object { $_.Extension -ieq ".gguf" })
    if ($routerModels.Count -eq 0) {
        Write-Host "创建路由配置需要 models 目录下至少有一个 .gguf 模型。" -ForegroundColor Red
        Read-Host "按 Enter 退出"
        exit 1
    }

    $mmprojFiles = @()
    if (Test-Path $mmprojRoot) {
        $mmprojFiles = @(Get-ChildItem -Path $mmprojRoot -Recurse -File -Include *.gguf | Sort-Object Name)
    }

    $manualRouterPreset = Join-Path $ScriptDir "router-models.ini"
    if (Test-Path $manualRouterPreset) {
        Write-Host ""
        Write-Host "检测到已有手动路由配置: $manualRouterPreset" -ForegroundColor Yellow
        $overwrite = Read-Default "是否覆盖并重新扫描生成？Y/N" "N"
        if ($overwrite -notmatch '^[Yy]') {
            Write-Host "已取消创建，原文件未修改。" -ForegroundColor Yellow
            Read-Host "按 Enter 退出"
            exit 0
        }
    }

    $autoMatchMmproj = $false
    if ($mmprojFiles.Count -gt 0) {
        $autoMatchChoice = Read-Default "是否按文件名前缀自动匹配 mmproj？Y/N" "Y"
        $autoMatchMmproj = $autoMatchChoice -match '^[Yy]'
    }

    Write-ManualRouterPreset -ModelFiles $routerModels -MmprojFiles $mmprojFiles -OutputPath $manualRouterPreset -AutoMatchMmproj $autoMatchMmproj

    Write-Host ""
    Write-Host "已创建手动路由配置: $manualRouterPreset" -ForegroundColor Green
    Write-Host "已扫描模型数量: $($routerModels.Count)"
    Write-Host "已扫描 mmproj 数量: $($mmprojFiles.Count)"
    if ($autoMatchMmproj) {
        Write-Host "已启用 mmproj 自动匹配；匹配成功的模型已自动填写 mmproj 和 image-min-tokens。" -ForegroundColor Cyan
    } else {
        Write-Host "未启用 mmproj 自动匹配；如需多模态模型，请编辑对应模型段落，取消注释 mmproj 和 image-min-tokens。" -ForegroundColor Cyan
    }
    Read-Host "按 Enter 退出"
    exit 0
}

if ($mode -eq "3") {
    $routerModels = @($models | Where-Object { $_.Extension -ieq ".gguf" })
    if ($routerModels.Count -eq 0) {
        Write-Host "路由模式需要 models 目录下至少有一个 .gguf 模型。" -ForegroundColor Red
        Read-Host "按 Enter 退出"
        exit 1
    }

    $manualRouterPreset = Join-Path $ScriptDir "router-models.ini"
    $autoRouterPreset = Join-Path $ScriptDir "router-models.auto.ini"
    Write-RouterPreset -ModelFiles $routerModels -OutputPath $autoRouterPreset

    $usingManualRouterPreset = Test-Path $manualRouterPreset
    $routerPreset = $autoRouterPreset

    if ($usingManualRouterPreset) {
        $routerPreset = $manualRouterPreset
        Write-Host ""
        Write-Host "已刷新自动路由配置: $autoRouterPreset" -ForegroundColor DarkCyan
        Write-Host "检测到手动路由配置，运行时使用且不会覆盖: $manualRouterPreset" -ForegroundColor Cyan
    }

    Write-Host ""
    if ($usingManualRouterPreset) {
        Write-Host "使用路由配置: $routerPreset" -ForegroundColor Green
    } else {
        Write-Host "已生成并使用自动路由配置: $routerPreset" -ForegroundColor Green
    }
    Write-Host "API 模型列表:"
    foreach ($modelFile in $routerModels) {
        $modelId = Get-ModelId $modelFile
        Write-Host "  - $modelId"
    }

    $hostValue = Read-Default "监听地址 --host" "127.0.0.1"
    $portValue = Read-Default "端口 --port" "29856"
    $uiChoice = Read-Default "是否启用 Web UI？Y/N" "N"
    $extraText = Read-Default "额外参数，留空跳过，例如 --models-max 2 --ctx-size 65536" ""
    $extraArgs = Split-CommandLine $extraText

    $commandArgs = @("--models-preset", $routerPreset, "--models-max", "1", "--models-autoload", "--host", $hostValue, "--port", $portValue)
    if ($uiChoice -match '^[Nn]') {
        $commandArgs += "--no-ui"
    }
    $commandArgs += $extraArgs

    if (-not (Show-FinalConfirm -Exe $serverExe -ArgumentList $commandArgs -Url "http://$hostValue`:$portValue")) {
        Write-Host "已取消启动。" -ForegroundColor Yellow
        Read-Host "按 Enter 退出"
        exit 0
    }
    & $serverExe @commandArgs

    Write-Host ""
    Write-Host "进程已结束。"
    Read-Host "按 Enter 退出"
    exit 0
}

$availableMmprojFiles = @()
if (Test-Path $mmprojRoot) {
    $availableMmprojFiles = @(Get-ChildItem -Path $mmprojRoot -Recurse -File -Include *.gguf | Sort-Object Name)
}

$recommendedModelIndex = 1
if ($mode -eq "1") {
    $recommendedModelIndex = Find-RecommendedModelIndex -ModelFiles $models -MmprojFiles $availableMmprojFiles
}

Write-Host ""
Write-Host "发现模型:"
for ($i = 0; $i -lt $models.Count; $i++) {
    $size = Format-Size $models[$i].Length
    $label = ""
    if ($mode -eq "1" -and ($i + 1) -eq $recommendedModelIndex -and $availableMmprojFiles.Count -gt 0) {
        $matchedMmproj = Find-MatchingMmproj -ModelFile $models[$i] -MmprojFiles $availableMmprojFiles
        if ($null -ne $matchedMmproj) {
            $label = "  [推荐: $($matchedMmproj.Name)]"
        }
    }
    Write-Host ("  {0,2}. {1}  ({2}){3}" -f ($i + 1), $models[$i].Name, $size, $label)
}

$modelIndex = Read-MenuChoice -Prompt "请选择模型编号" -Default $recommendedModelIndex -Min 1 -Max $models.Count

$model = $models[$modelIndex - 1]

$mmprojPath = ""
$imageMinTokens = ""
if ($mode -ne "4") {
    $recommendedMmproj = Find-MatchingMmproj -ModelFile $model -MmprojFiles $availableMmprojFiles
    $mmprojPath = Select-Mmproj -Directory $mmprojRoot -ModelName $model.Name -RecommendedMmproj $recommendedMmproj
    if ([string]::IsNullOrWhiteSpace($mmprojPath)) {
        $mmprojPath = Read-Default "其他 mmproj 路径，留空跳过；可输入完整路径，例如 D:\models\mmproj-BF16.gguf" ""
    }
    if (-not [string]::IsNullOrWhiteSpace($mmprojPath)) {
        $mmprojPath = Resolve-InputPath $mmprojPath
        if (-not (Test-Path $mmprojPath)) {
            Write-Host "找不到 mmproj 文件：$mmprojPath" -ForegroundColor Red
            Read-Host "按 Enter 退出"
            exit 1
        }

        $imageMinTokens = Read-Default "图片最小 token 数 --image-min-tokens，默认 1024；输入 0 或 N 跳过" "1024"
        if ($imageMinTokens -match '^[Nn]$' -or $imageMinTokens -eq "0") {
            $imageMinTokens = ""
        } elseif (-not ($imageMinTokens -match '^\d+$')) {
            Write-Host "--image-min-tokens 需要输入数字、0 或 N。" -ForegroundColor Red
            Read-Host "按 Enter 退出"
            exit 1
        }
    }
}

$commonArgs = @("-m", $model.FullName)

$ctx = Read-Default "上下文长度 --ctx-size，留空用模型默认，或输入如 4096/8192" ""
if (-not [string]::IsNullOrWhiteSpace($ctx)) {
    $commonArgs += @("--ctx-size", $ctx)
}

$gpuLayers = Read-Default "GPU 层数 --n-gpu-layers，默认 auto，可输入 all/0/具体数字" "auto"
if (-not [string]::IsNullOrWhiteSpace($gpuLayers)) {
    $commonArgs += @("--n-gpu-layers", $gpuLayers)
}

if (-not [string]::IsNullOrWhiteSpace($mmprojPath)) {
    $commonArgs += @("--mmproj", $mmprojPath)
    if (-not [string]::IsNullOrWhiteSpace($imageMinTokens)) {
        $commonArgs += @("--image-min-tokens", $imageMinTokens)
    }
}

$extraText = Read-Default "额外参数，留空跳过，例如 --threads 8 --temp 0.7" ""
$extraArgs = Split-CommandLine $extraText

switch ($mode) {
    "4" {
        if (-not (Test-Path $cliExe)) {
            Write-Host "找不到 llama-cli.exe 或 llama.exe，无法启动命令行聊天。" -ForegroundColor Red
            Read-Host "按 Enter 退出"
            exit 1
        }

        $commandArgs = $commonArgs + @("-cnv") + $extraArgs
        if (-not (Show-FinalConfirm -Exe $cliExe -ArgumentList $commandArgs)) {
            Write-Host "已取消启动。" -ForegroundColor Yellow
            Read-Host "按 Enter 退出"
            exit 0
        }
        & $cliExe @commandArgs
    }
    default {
        $hostValue = Read-Default "监听地址 --host" "127.0.0.1"
        $portValue = Read-Default "端口 --port" "29856"
        $uiChoice = Read-Default "是否启用 Web UI？Y/N" "N"

        $commandArgs = $commonArgs + @("--host", $hostValue, "--port", $portValue)
        if ($uiChoice -match '^[Nn]') {
            $commandArgs += "--no-ui"
        }
        $commandArgs += $extraArgs

        if (-not (Show-FinalConfirm -Exe $serverExe -ArgumentList $commandArgs -Url "http://$hostValue`:$portValue")) {
            Write-Host "已取消启动。" -ForegroundColor Yellow
            Read-Host "按 Enter 退出"
            exit 0
        }
        & $serverExe @commandArgs
    }
}

Write-Host ""
Write-Host "进程已结束。"
Read-Host "按 Enter 退出"
