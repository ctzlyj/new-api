[CmdletBinding()]
param(
    [string]$HostName = $env:LTS4AI_DEPLOY_HOST,
    [string]$UserName = 'ltsadmin',
    [Parameter(Mandatory = $true)]
    [string]$IdentityFile,
    [Parameter(Mandatory = $true)]
    [string]$RemoteBackupCommand
)

$ErrorActionPreference = 'Stop'
if ([string]::IsNullOrWhiteSpace($HostName)) {
    throw 'HostName or LTS4AI_DEPLOY_HOST is required.'
}
$identityPath = (Resolve-Path -LiteralPath $IdentityFile).Path
$target = "$UserName@$HostName"
$output = & ssh -o BatchMode=yes -o StrictHostKeyChecking=yes -i $identityPath $target $RemoteBackupCommand
if ($LASTEXITCODE -ne 0) {
    throw "Remote backup failed with exit code $LASTEXITCODE."
}
$matches = [regex]::Matches(($output -join "`n"), '/opt/new-api/deploy/backups/mysql-new_api-[0-9TZ]+\.sql\.gz')
if ($matches.Count -eq 0) {
    throw 'Remote backup did not return the expected backup path.'
}
$backupPath = $matches[$matches.Count - 1].Value
& ssh -o BatchMode=yes -o StrictHostKeyChecking=yes -i $identityPath $target "gzip -t '$backupPath'"
if ($LASTEXITCODE -ne 0) {
    throw 'Remote backup integrity verification failed.'
}
Write-Output $backupPath