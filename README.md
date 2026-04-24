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

## この環境で `go test ./...` を通すために必要なもの

詳細は `docs.md` を参照してください。

- `proxy.golang.org` または GitHub への HTTPS アクセス
- `terraform-plugin-sdk/v2` と `terraform-plugin-log` の module 取得許可
- 外向き通信不可なら `go mod vendor` 済みソースか社内 GOPROXY

## テスト

```bash
go test ./...
```

## API ドキュメント

- https://indigo.arena.ne.jp/userapi/


## examples

- `examples/ssh-key-vm`: SSH 鍵作成 + VM 作成の一連の Terraform 例

実行例:

```bash
cd examples/ssh-key-vm
cp terraform.tfvars.example terraform.tfvars
terraform init
terraform plan
terraform apply
```


## `terraform init` で provider が見つからない場合

原因は Terraform の provider 取得モードです。`local/indigo` は未公開 provider なので、`dev_overrides` だけだと `terraform init` 中に registry 問い合わせが走って失敗することがあります。（表示される Warning のとおり、`dev_overrides` 利用時は `init` をスキップする運用が前提です）

`terraform init` も通したい場合は、`~/.terraformrc` を **filesystem_mirror** 方式にしてください。

```hcl
provider_installation {
  filesystem_mirror {
    path    = "~/.terraform.d/plugins"
    include = ["local/indigo"]
  }
  direct {
    exclude = ["local/indigo"]
  }
}
```

その後に再初期化します。

```bash
rm -rf .terraform .terraform.lock.hcl
terraform init
terraform providers
```

補足: `dev_overrides` を使う場合は Warning のとおり `terraform init` を省略し、`plan/apply` を直接実行してください。


## API ドキュメント

- [WebARENA Indigo API](https://indigo.arena.ne.jp/userapi/)
