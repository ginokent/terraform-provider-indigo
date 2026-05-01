# Terraform Provider: WebARENA Indigo

WebARENA Indigo API (`https://api.customer.jp/webarenaIndigo/v1`) 向けの Terraform Provider です。

## 特徴

- `indigo_ssh_key` リソースで SSH 公開鍵を作成/更新/削除
- `indigo_instance` リソースでインスタンスの作成/参照/削除
- `indigo_instance_types` データソースでインスタンスタイプ一覧取得
- `indigo_regions` データソースでリージョン一覧取得
- `indigo_oses` データソースで OS 一覧取得
- `indigo_instance_specs` データソースでプラン(スペック)一覧取得
- Indigo API の品質問題（レスポンス shape 不一致、typo、曖昧なエラー）を吸収するための防御的デコード

## Provider 設定

```hcl
terraform {
  required_providers {
    indigo = {
      source  = "local/indigo"
      version = "0.1.0"
    }
  }
}

provider "indigo" {
  api_key    = var.indigo_api_key
  api_secret = var.indigo_api_secret

  # optional overrides
  # oauth_endpoint  = "https://api.customer.jp/oauth/v1"
  # indigo_endpoint = "https://api.customer.jp/webarenaIndigo/v1"
}
```

## API 鍵の設定方法（環境変数対応）

はい、環境変数から設定できます。

- `WEBARENA_INDIGO_API_KEY`
- `WEBARENA_INDIGO_API_SECRET`
- （任意）`WEBARENA_INDIGO_OAUTH_ENDPOINT`
- （任意）`WEBARENA_INDIGO_ENDPOINT`

```bash
export WEBARENA_INDIGO_API_KEY="<your-api-key>"
export WEBARENA_INDIGO_API_SECRET="<your-api-secret>"
```

この場合、provider ブロックは空でも動作します（API key/secret は環境変数から取得）。

```hcl
provider "indigo" {}
```

明示指定したい場合は、従来どおり provider ブロックへ直接書けます。

## リソース例

```hcl
resource "indigo_instance" "vm" {
  name       = "tf-example"
  region_id  = 1
  os_id      = 22
  plan_id    = 13
  ssh_key_id = 21633
}
```


## ID の調べ方（region_id / os_id / plan_id）

次の data source で Terraform 内から取得できます。

```hcl
data "indigo_instance_types" "all" {}

data "indigo_regions" "all" {
  instance_type_id = data.indigo_instance_types.all.instance_types[0].id
}

data "indigo_oses" "all" {
  instance_type_id = data.indigo_instance_types.all.instance_types[0].id
}

data "indigo_instance_specs" "all" {
  instance_type_id = data.indigo_instance_types.all.instance_types[0].id
  os_id            = data.indigo_oses.all.oses[0].id
}
```

- `instance_type_id` は `data.indigo_instance_types.all.instance_types[*].id`
- `region_id` は `data.indigo_regions.all.regions[*].id`
- `os_id` は `data.indigo_oses.all.oses[*].id`
- `plan_id` は `data.indigo_instance_specs.all.instance_specs[*].id`


## 取得手順（APIで確認）

Terraform data source を使わず、API で直接確認する場合の手順です。

```bash
# 1) アクセストークン取得
TOKEN=$(curl -s -X POST https://api.customer.jp/oauth/v1/accesstokens -H 'Content-Type: application/json' -d '{"grantType":"client_credentials","clientId":"'"$WEBARENA_INDIGO_API_KEY"'","clientSecret":"'"$WEBARENA_INDIGO_API_SECRET"'","code":""}' | jq -r '.accessToken')

# 2) instance_type_id 候補
curl -s -H "Authorization: Bearer $TOKEN" "https://api.customer.jp/webarenaIndigo/v1/vm/instancetypes" | jq
# {
#   "success": true,
#   "total": 2,
#   "instanceTypes": [
#     {
#       "id": 1,
#       "name": "instance",
#       "display_name": "KVM Instance",
#       "created_at": "2019-10-09 13:11:20",
#       "updated_at": "2019-10-09 13:11:20"
#     },
#     {
#       "id": 2,
#       "name": "microvps",
#       "display_name": "MicroVPS",
#       "created_at": "2019-10-09 13:11:20",
#       "updated_at": "2019-10-09 13:11:20"
#     }
#   ]
# }

# 3) region_id 候補
INSTANCE_TYPE_ID=1
curl -s -H "Authorization: Bearer $TOKEN" "https://api.customer.jp/webarenaIndigo/v1/vm/getregion?instanceTypeId=$INSTANCE_TYPE_ID" | jq
# {
#   "success": true,
#   "total": 1,
#   "regionlist": [
#     {
#       "id": 1,
#       "oem_id": 1,
#       "name": "Tokyo",
#       "use_possible_date": "2019-10-03 23:07:19"
#     }
#   ]
# }

# 4) os_id 候補
INSTANCE_TYPE_ID=1
curl -s -H "Authorization: Bearer $TOKEN" "https://api.customer.jp/webarenaIndigo/v1/vm/oslist?instanceTypeId=$INSTANCE_TYPE_ID" | jq
# {
#   "success": true,
#   "total": 9,
#   "osCategory": [
#     {
#       "id": 1,
#       "name": "Ubuntu",
#       "logo": "Ubudu.png",
#       "osLists": [
#         ...
#         {
#           "id": 25,
#           "categoryid": 1,
#           "code": "Ubuntu",
#           "name": "Ubuntu24.04",
#           "viewname": "Ubuntu 24.04",
#           "instancetype_id": 1
#         }
#       ]
#     },
#     ...
#   ]
# }

# 5) plan_id 候補（osId を指定）
OS_ID=25
curl -s -H "Authorization: Bearer $TOKEN" "https://api.customer.jp/webarenaIndigo/v1/vm/getinstancespec?instanceTypeId=1&osId=$OS_ID" | jq
# {
#   "success": true,
#   "total": 7,
#   "speclist": [
#     ...
#     {
#       "id": 3,
#       "code": "4vCPU4GB80GB",
#       "name": "Memory4GB,4vCPU,SSD80GB",
#       "description": "<b>4</b> vCPU <br> <b>4 GB</b> RAM <br> <b>80 GB</b> SSD <br><b>500 </b> Mbps",
#       "use_possible_date": "2019-11-19 00:00:00",
#       "instancetype_id": 1,
#       "ipaddress_type": "ipv46dual",
#       "created_at": "2019-10-09 13:11:49",
#       "updated_at": "2019-10-09 13:11:49",
#       "instance_type": {
#         "id": 1,
#         "name": "instance",
#         "display_name": "KVM Instance",
#         "created_at": "2019-10-09 13:11:20",
#         "updated_at": "2019-10-09 13:11:20"
#       },
#       "kvm_resources": {
#         "id": 15,
#         "plan_id": 3,
#         "name": "4CR4GB",
#         "param": "vcpus",
#         "limitnum": 4
#       }
#     },
#     ...
#   ]
# }

# 6) ssh_key_id 取得
curl -s -H "Authorization: Bearer $TOKEN" "https://api.customer.jp/webarenaIndigo/v1/vm/sshkey" | jq
# {
#   "success": true,
#   "total": 1,
#  "sshkeys": [
#    {
#      "id": ...,
#      "service_id": ...,
#      "user_id": ...,
#      "name": ...,
#      "sshkey": "ssh-rsa AAAAB3NzaC1yc2EAAAADAQABAAACAQC...",
#      "status": "ACTIVE",
#      "created_at": "2025-05-25 11:26:31",
#      "updated_at": "2025-05-25 11:26:31"
#    }
#  ]
# }

# 7) vm 一覧取得
curl -s -H "Authorization: Bearer $TOKEN" "https://api.customer.jp/webarenaIndigo/v1/vm/getinstancelist" | jq
```

## ローカルインストール（`make install`）

```bash
make install
```

初回セットアップ時は必要に応じて `make deps` を先に実行してください。

上記で Terraform CLI のローカル provider ディレクトリへバイナリを配置します。

- インストール先: `~/.terraform.d/plugins/registry.terraform.io/local/indigo/<version>/<os>_<arch>/terraform-provider-indigo_v<version>`
- 既定 version: `0.1.0`（`VERSION=0.1.1 make install` のように上書き可能）

```hcl
terraform {
  required_providers {
    indigo = {
      source  = "local/indigo"
      version = "0.1.0"
    }
  }
}
```

## ビルド / テスト環境の要件

`go test ./...` / `go build ./...` を通すには次が必要です。

1. **Go module の取得許可**
   - `proxy.golang.org` への HTTPS アクセス（推奨）、もしくは GitHub への直接 HTTPS アクセス（`GOPROXY=direct` 利用時）
2. **HashiCorp モジュールの取得**
   - `github.com/hashicorp/terraform-plugin-sdk/v2`
   - `github.com/hashicorp/terraform-plugin-log`

### 推奨設定（社内プロキシがある場合）

```bash
go env -w GOPROXY=https://proxy.golang.org,direct
go env -w GOSUMDB=sum.golang.org
```

### 完全に外向き通信不可の環境での選択肢

- 接続可能な環境で `go mod vendor` を実施し、`vendor/` を同梱してからビルド
- あるいは社内の Go module mirror を用意し、`GOPROXY` をそこへ向ける

### 疎通確認

```bash
go mod download
go test ./...
```

## API ドキュメント

- https://indigo.arena.ne.jp/userapi/


## examples

- `examples/ssh-key-vm`: SSH 鍵作成 + VM 作成の一連の Terraform 例

実行例 (`~/.terraformrc` の `filesystem_mirror` 設定済みを前提とする。詳細は次節):

```bash
cd examples/ssh-key-vm
cp terraform.tfvars.example terraform.tfvars
terraform init
terraform plan
terraform apply
```


## `~/.terraformrc` の設定

未公開 provider のため、Terraform CLI に `local/indigo` の解決方法を教える必要があります。`~/.terraformrc` (環境変数 `TF_CLI_CONFIG_FILE` でも可) に `filesystem_mirror` を設定してください。

```hcl
provider_installation {
  filesystem_mirror {
    path    = "<HOME>/.terraform.d/plugins"
    include = ["local/indigo"]
  }
  direct {
    exclude = ["local/indigo"]
  }
}
```

- `<HOME>` は環境のホームディレクトリ絶対パスに置換 (Terraform CLI configuration は `~` / `$HOME` を展開しない)
- `path` は `make install` の配置先と同じディレクトリ (`~/.terraform.d/plugins`)。サブディレクトリ `registry.terraform.io/local/indigo/<version>/<os>_<arch>/` 以下に置かれたバイナリが解決される
- `direct.exclude` で `local/indigo` だけ registry 問い合わせから外す。それ以外の provider は通常通り registry から取得される

`make install` を実行すると **当該環境の絶対パスを埋め込んだ tfrc 例** がメッセージで表示されるので、それをそのまま貼り付けるのが確実です。

### 運用上の注意

- 設定変更後は `.terraform/` と `.terraform.lock.hcl` の不整合で `terraform init` が失敗することがあるため、その場合は削除して再実行する

```bash
rm -rf .terraform .terraform.lock.hcl
terraform init
terraform plan
```

## API ドキュメント

- [WebARENA Indigo API](https://indigo.arena.ne.jp/userapi/)
