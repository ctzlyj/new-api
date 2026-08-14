# Qiniu model synchronization operations

This runbook deploys the Qiniu catalog synchronizer without exposing the upstream API key. The key must remain in the production database and is copied server-side from channel `7` into the dedicated managed channel.

## Configuration

Set these values in `/opt/new-api/deploy/.env`:

```dotenv
QINIU_MODEL_SYNC_ENABLED=true
QINIU_MODEL_SYNC_INTERVAL_HOURS=24
QINIU_RESOURCE_PACKAGE_COST_CNY_PER_100M=323
QINIU_RESOURCE_PACKAGE_SALE_CNY_PER_100M=350
QINIU_POINTS_PER_CNY=20
QINIU_MANAGED_CHANNEL_TAG=qiniu-managed
```

The managed channel must use:

- Type: OpenAI
- Base URL: `https://api.qnaigc.com`
- Tag: `qiniu-managed`
- Status: enabled
- Models: initially empty
- Key: copied from channel `7` entirely inside MySQL; never echo or export it

Exactly one channel may have the managed tag. `SF OpenAI Upstream` remains separate and keeps `SF-gpt-image-2` until cutover verification finishes.

Resource-package billing uses Qiniu's official formula:

```text
deduction ratio = target CNY price per 1K tokens / 0.004 CNY
points per 1M actual tokens = deduction ratio * 70
```

The defaults sell 100M deduction tokens for 350 CNY / 7000 points against a 323 CNY package cost, producing a 7.71% gross margin. The synchronizer reads the live custom display rate to convert points into the gateway's internal quota currency.

## Safety gates

Do not disable any legacy text channel until all gates pass:

1. A fresh database backup exists and `gzip -t` succeeds.
2. The new immutable image is healthy.
3. The first `qiniu_model_sync` task succeeds.
4. The accepted model count is non-zero and plausible.
5. `SF-gpt-image-2` remains visible and image-capable.
6. A low-token Qiniu text request succeeds.
7. The charged quota equals the official resource-package deduction ratio at 7000 points per 100M deduction tokens.
8. Image Studio still reads the shared catalog and account quota.

The synchronizer refuses empty callable or accepted snapshots. A fetch, validation, ownership collision, billing smoke-test, or transaction failure preserves the previous snapshot.

## Build

From the final reviewed commit:

```powershell
$env:GOOS='linux'
$env:GOARCH='amd64'
$env:CGO_ENABLED='0'
$commit = git rev-parse --short HEAD
$version = "qiniu-sync-20260813-$commit"
go build -trimpath -ldflags "-s -w -X 'github.com/QuantumNous/new-api/common.Version=$version'" -o build/qiniu-sync/new-api-linux-amd64 .
```

Upload the binary to a temporary server path. Build an overlay image from the currently deployed customized image, not from upstream:

```bash
docker build --build-arg BASE_IMAGE=new-api-local:sf-wallet-20260805 \
  -t new-api-local:qiniu-sync-20260813-<commit> \
  -f ops/qiniu-model-sync/Dockerfile.binary-overlay \
  ops/qiniu-model-sync
```

Copy the binary into the Docker build context as `new-api` before building. Update the production Compose override to the immutable new tag and retain the previous tag for rollback.

## Backup

Run `backup.ps1` from the workstation. It executes a pre-existing, reviewed server-side backup command and verifies the returned `.sql.gz` path without printing credentials.

```powershell
$env:LTS4AI_DEPLOY_HOST='<host>'
.\ops\qiniu-model-sync\backup.ps1 \
  -IdentityFile 'D:\caotong.888\Desktop\lts4ai\lts4ai_admin_ed25519' \
  -RemoteBackupCommand 'sudo bash /opt/new-api/deploy/scripts/backup_mysql.sh'
```

If the production backup command has a different path, pass that exact reviewed command. Never pass database passwords on the command line.

## Managed channel creation

Run the operation inside MySQL so the key never leaves the server. Use the actual production schema discovered immediately before deployment. The operation must:

1. Lock channel `7`.
2. Confirm no channel already uses `qiniu-managed`.
3. Insert one enabled OpenAI channel with base URL `https://api.qnaigc.com`, empty models, and the copied key.
4. Commit without selecting or printing the key.

Do not create abilities for an empty channel. The first successful sync creates the abilities transactionally.

## Deployment and verification

1. Update `.env` with the six configuration values.
2. Start the new image with the existing Compose file set.
3. Check `/api/status` and container health.
4. Confirm the startup task has type `qiniu_model_sync` and status `succeeded`.
5. Confirm the public model list contains accepted Qiniu text models and `SF-gpt-image-2`.
6. Confirm retired, non-OpenAI, image, and unpriced Qiniu models are absent.
7. Run `verify.ps1` without `-RunBillableProbe`.
8. Record a probe user's quota, run one low-token text request with `-RunBillableProbe`, and record quota again.
9. Compare the deduction with `billing_setting.billing_expr`: each deduction-ratio unit must cost 70 points per million actual tokens.
10. Rebuild and restart the Bridge, then verify Image Studio catalog and account views.

## Cutover

After every safety gate passes:

- Keep old channels and rows for history, but disable and hide legacy text channels.
- Restrict channel `7` to `SF-gpt-image-2` only.
- Remove legacy text abilities from channel `7` through the normal channel update path.
- Do not delete channels, models, logs, or historical usage.

## Rollback

1. Repoint Compose to `new-api-local:sf-wallet-20260805`.
2. Restart New API and verify health.
3. Disable the `qiniu-managed` channel without deleting it.
4. Re-enable the previous text channel if it had already been disabled.
5. Restore the database backup only if schema or data state cannot be corrected safely in place.
6. Restore the previous Bridge build if catalog capability parsing regresses.

Rollback must not overwrite or delete `SF-gpt-image-2`, user quota, usage logs, or historical channels.