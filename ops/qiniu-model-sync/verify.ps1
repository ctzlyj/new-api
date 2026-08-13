[CmdletBinding()]
param(
    [string]$ApiBase = 'https://api.lts4ai.com',
    [string]$AccessToken = $env:LTS4AI_PROBE_TOKEN,
    [string]$TextModel,
    [switch]$RunBillableProbe
)

$ErrorActionPreference = 'Stop'
$base = $ApiBase.TrimEnd('/')
$status = Invoke-RestMethod -Method Get -Uri "$base/api/status" -TimeoutSec 30
if ($null -eq $status) {
    throw 'Status endpoint returned no data.'
}
if ([string]::IsNullOrWhiteSpace($AccessToken)) {
    throw 'AccessToken or LTS4AI_PROBE_TOKEN is required.'
}
$headers = @{ Authorization = "Bearer $AccessToken" }
$catalog = Invoke-RestMethod -Method Get -Uri "$base/v1/models" -Headers $headers -TimeoutSec 60
$modelIds = @($catalog.data | ForEach-Object { $_.id })
if ($modelIds -notcontains 'SF-gpt-image-2') {
    throw 'SF-gpt-image-2 is missing from the public model catalog.'
}
$textModels = @($modelIds | Where-Object { $_ -ne 'SF-gpt-image-2' })
if ($textModels.Count -eq 0) {
    throw 'No Qiniu text model is visible in the public catalog.'
}
if (-not $RunBillableProbe) {
    [pscustomobject]@{
        StatusChecked = $true
        ModelCount = $modelIds.Count
        TextModelCount = $textModels.Count
        ImageModelPresent = $true
        BillableProbeRan = $false
    }
    return
}
if ([string]::IsNullOrWhiteSpace($TextModel)) {
    throw 'TextModel is required with RunBillableProbe.'
}
if ($modelIds -notcontains $TextModel) {
    throw "Text model '$TextModel' is not in the public catalog."
}
$body = @{
    model = $TextModel
    messages = @(@{ role = 'user'; content = 'Reply with OK only.' })
    max_tokens = 4
    temperature = 0
} | ConvertTo-Json -Depth 5
$response = Invoke-RestMethod -Method Post -Uri "$base/v1/chat/completions" -Headers $headers -ContentType 'application/json' -Body $body -TimeoutSec 120
if ($null -eq $response.choices -or @($response.choices).Count -eq 0) {
    throw 'Billable text probe returned no choices.'
}
[pscustomobject]@{
    StatusChecked = $true
    ModelCount = $modelIds.Count
    TextModelCount = $textModels.Count
    ImageModelPresent = $true
    BillableProbeRan = $true
    ProbeModel = $TextModel
    PromptTokens = $response.usage.prompt_tokens
    CompletionTokens = $response.usage.completion_tokens
}