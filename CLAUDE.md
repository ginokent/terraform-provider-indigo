# CLAUDE.md

WebARENA Indigo (KVM VPS) 向け Terraform Provider。`terraform-plugin-sdk/v2` ベース。Go 1.24。

## 重要な前提

- **未公開 provider**: `local/indigo` source。`make install` でローカル plugin ディレクトリへ配置する運用。
- **Indigo API は品質が悪い**: レスポンスの shape 不一致、key の typo、曖昧なエラーメッセージが頻発する。これを吸収するための防御的実装が provider/client の各所に意図的に組み込まれている (削るな)。

## エントリーポイント / 構成要点

- `main.go` (リポジトリルート) … `plugin.Serve` のみ。`Makefile` の `build`/`install` も `.` を対象にする
- `internal/provider/provider.go` … schema 定義、`configure` で API key 必須チェックと初回 probe (`ListRegions`)
- `internal/provider/resource_instance.go` … インスタンス CRUD。`status` (API 状態, computed) と `instance_status` (電源状態 running/stopped, Optional+Computed) を分離している点が中心
- `internal/provider/resource_ssh_key.go` … SSH 鍵 CRUD
- `internal/client/client.go` … HTTP client。OAuth2 client_credentials → Bearer。レート制限 600ms、429/5xx 時に最大 5 回リトライ、`Retry-After` 尊重

## 防御的デコードのパターン (削除/簡略化禁止)

- `decodeViaMarshal`: `json.Marshal` → `Unmarshal` で型変換
- 単体オブジェクト/配列のどちらでも来るので、両方試して一致した方を採用 (`decodeInstance`, `decodeSSHKey`)
- レスポンス key の typo/揺れに対応するため複数候補 key を順に見る (`InstanceTypes`/`InstanceType`/`TypeList`/`TypeListAlt` 等)
- `ListInstanceTypes` は endpoint 候補を複数試行して 404 をスキップ
- Instance の `UnmarshalJSON` で `instance_name`, `instancestatus` (typo), `ipaddress`/`ip` フォールバック、`status` を `APIStatus` と `Status` の両方に保存
- `resourceInstanceRead` で `RegionID`/`OSID`/`PlanID`/`SSHPublicKey` が API から 0 で返る既知バグに対し state 値を維持する (perpetual drift 回避)

## ステータス制御の要点

- `instance_status` は Optional+Computed+StateFunc(ToLower)+ValidateFunc("running"|"stopped" のみ)
- `normalizePowerStatus`: API は `running`/`active`/`ready`/`start` → `running`、`stopped`/`stop`/`forcestop`/`close`/`closed`/`open` → `stopped` に正規化
- `UpdateContext` は `instance_status` の差分のみで `start`/`stop` を `/vm/instance/statusupdate` に POST
- `isIdempotentStatusUpdateError`: 400 + "already running"/"already stopped" を成功扱いに
- `DeleteInstance` 実装は `UpdateInstanceStatus(id, "destroy")` (専用 DELETE エンドポイントは存在しない)
- 削除後は `GetInstanceByID` を 2 分間 retry して消滅確認

## Provider 設定

- 認証: `api_key`/`api_secret` (必須) または env `WEBARENA_INDIGO_API_KEY`/`WEBARENA_INDIGO_API_SECRET`
- endpoint override: `oauth_endpoint`/`indigo_endpoint` または env `WEBARENA_INDIGO_OAUTH_ENDPOINT`/`WEBARENA_INDIGO_ENDPOINT`
- default: `https://api.customer.jp/oauth/v1`, `https://api.customer.jp/webarenaIndigo/v1`

## リソース / データソース

- resource: `indigo_instance` (name/region_id/os_id/plan_id/ssh_key_id すべて ForceNew、status/status_raw/ipv4 computed、instance_status 可変)、`indigo_ssh_key`
- data source: `indigo_instance_types`, `indigo_regions`, `indigo_oses`, `indigo_instance_specs` (それぞれ `instance_type_id` / `os_id` を引数に取るものあり)

## 開発フロー

- `go test ./...` (全体) / `make test-client` (client のみ)
- `make install` でローカルにインストール (`~/.terraform.d/plugins/registry.terraform.io/local/indigo/<ver>/<os>_<arch>/`)
- `terraform init` を通すには `~/.terraformrc` を `filesystem_mirror` 方式にする (README 末尾参照)
- examples: `examples/ssh-key-vm/` (`terraform.tfvars.example` をコピーして使う)

## エラー表示

- `client.APIError` に `StatusCode`/`Method`/`Endpoint`/`Message`/`Body`/`Hint`
- `errorHint`: 429 は parallelism 削減を促す。400 + (`I10037` / "license failed to update") は契約状態ヒント
- `extractAPIErrorMessage` は再帰的に `message`/`error`/`detail`/`details`/`errors`/`validationErrors` を拾う
- provider 層では `opDiag(resourceType, operation, err)` でラップ統一

## 作業上の注意

- 後方互換性 / fallback は不要 (ユーザー方針)。意味的に正しい実装を優先
- 既存テストの粒度に合わせず適切にテストを書く
- ドキュメントは日本語、設計大方針は `SPEC.ja.md` (現状なし)、詳細はコード内コメント
- README に「品質問題を吸収するための防御的デコード」と明記されているとおり、ここは設計意図。簡略化リファクタを提案する前に必ず確認する
