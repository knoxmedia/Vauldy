# sync-vauldy.ps1 - Sync knox-media with Vauldy upstream
# Usage:
#   .\sync-vauldy.ps1 pull  # Merge Vauldy into knox-media
#   .\sync-vauldy.ps1 pr    # Create PR branch for Vauldy
#   .\sync-vauldy.ps1 diff  # Show diff between repos

param(
    [Parameter(Mandatory=$true)]
    [ValidateSet('pull','pr','diff')]
    [string]$Action
)

$ErrorActionPreference = 'Stop'
Set-Location (Split-Path -Parent $PSScriptRoot)

function Test-UpstreamRemote {
    if ((git remote) -notcontains 'upstream') {
        Write-Host 'ERROR: upstream not found. Run:' -ForegroundColor Red
        Write-Host '  git remote add upstream https://github.com/knoxmedia/Vauldy.git' -ForegroundColor Yellow
        exit 1
    }
}

switch ($Action) {
    'pull' {
        Test-UpstreamRemote
        Write-Host '>>> Pulling Vauldy community code into knox-media...' -ForegroundColor Cyan
        git fetch upstream
        git checkout community
        git merge upstream/main --no-edit
        git checkout main
        git merge community --no-edit
        if ($LASTEXITCODE -ne 0) {
            Write-Host 'CONFLICT: Resolve manually, then: git add . && git commit' -ForegroundColor Yellow
            exit 1
        }
        Write-Host '>>> Done. Run: git push origin main' -ForegroundColor Green
    }
    'pr' {
        Test-UpstreamRemote
        $ts = Get-Date -Format 'yyyyMMdd-HHmmss'
        $branch = 'upstream/' + $ts
        Write-Host ">>> Creating PR branch '$branch' for Vauldy..." -ForegroundColor Cyan
        git fetch upstream
        git checkout community
        git merge upstream/main --no-edit
        git checkout -b $branch
        Write-Host ">>> Branch '$branch' created." -ForegroundColor Green
        Write-Host ''
        Write-Host 'Next steps:' -ForegroundColor Yellow
        Write-Host '  1. Cherry-pick commits: git cherry-pick <hash>' -ForegroundColor White
        Write-Host "  2. Push to Vauldy:     git push upstream $branch" -ForegroundColor White
        Write-Host "  3. Create PR at:       https://github.com/knoxmedia/Vauldy/compare/main...knoxlab:knox-media:$branch" -ForegroundColor White
        Write-Host ''
    }
    'diff' {
        Test-UpstreamRemote
        Write-Host '>>> Diff: knox-media main vs Vauldy upstream/main' -ForegroundColor Cyan
        git fetch upstream
        git diff --stat main upstream/main
    }
}
