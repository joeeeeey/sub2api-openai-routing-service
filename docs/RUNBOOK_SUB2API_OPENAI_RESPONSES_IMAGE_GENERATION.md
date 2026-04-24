# RUNBOOK_SUB2API_OPENAI_RESPONSES_IMAGE_GENERATION

## 目标

统一使用 `POST /openai-routing/v1/responses` 的 `image_generation` tool 调用 OpenAI `gpt-image-2`。

这份文档只推荐 `/v1/responses` 方式，不再把 `/v1/images/generations` 作为主用法。

## 已验证状态

在当前分支上，本地已验证：

- 文本模型回归可通过：
  - `gpt-5.1`
  - `gpt-5.2`
  - `gpt-5.4`
  - `gpt-5.5`
- `/openai-routing/v1/responses` + `image_generation` 可成功返回：
  - `status=completed`
  - `output[0].type=image_generation_call`
  - `output[0].result` 为图片 base64

## 请求模型规则

图片生成要分两层模型：

1. 顶层 `model`

- 必须是 Responses-capable 文本模型
- 推荐：`gpt-5.4-mini`

2. `tools[0].model`

- 必须是图片模型
- 当前推荐：`gpt-image-2`

错误示例：

- 顶层 `model` 直接写 `gpt-image-2`

这会被当前分支拦截并返回：

- `/v1/responses image_generation requests require a Responses-capable text model`

## 当前分支支持的 `image_generation` tool 参数

以下字段会被透传到上游 `image_generation` tool：

- `model`
- `size`
- `quality`
- `background`
- `output_format`
- `moderation`
- `style`
- `output_compression`
- `partial_images`

兼容字段：

- `format` 会被自动改写为 `output_format`
- `compression` 会被自动改写为 `output_compression`

## 参数说明

### `size`

当前代码不做本地白名单校验，会透传给上游。

本分支实际已覆盖/验证过的值：

- `1024x1024`
- `1536x1024`

如果你要用其他尺寸，是否可用取决于上游 `gpt-image-2` 当前支持情况。

### `output_format`

当前代码已验证：

- `png`
- `webp`

其他格式没有在当前分支里做本地白名单校验，但是否可用仍取决于上游。

### `background`

当前代码已验证：

- `auto`
- `transparent`

### `quality`

当前代码已验证：

- `high`

### `partial_images`

当前代码会透传，并且在 `stream=true` 时支持接收：

- `image_generation.partial_image`
- `image_generation.completed`

含义：

- `partial_images` 控制上游在图片生成过程中是否返回阶段性预览图
- 它本身不会让非流式请求更快返回
- 只有和 `stream=true` 一起使用时，这个参数才真正有意义

建议：

- 如果你只关心最终图片：
  - `stream=false`
  - 不必设置 `partial_images`
- 如果你希望前端或脚本尽早看到“预览图 / 中间图”：
  - `stream=true`
  - 同时设置 `partial_images`

一个实用理解：

- `stream=true`：决定“响应是否边生成边返回”
- `partial_images`：决定“边返回时，是否把阶段性图片也一起发出来”

## 输入图片 / 垫图能力

支持。

如果你不只是 `input_text`，还想上传一张或多张参考图作为 prompt，请在顶层 `input[0].content[]` 中追加多个 `input_image`。

推荐方式：

- 第一张图做构图参考
- 第二张图做颜色/风格参考
- 再配合一段 `input_text` 做约束说明

## 最小可运行示例

```bash
curl -s http://127.0.0.1:8080/openai-routing/v1/responses \
  -H "Authorization: Bearer $OPENAI_ROUTING_UI_API_KEY" \
  -H 'Content-Type: application/json' \
  -d '{
    "model":"gpt-5.4-mini",
    "input":[
      {
        "type":"message",
        "role":"user",
        "content":[
          {"type":"input_text","text":"A minimal red square centered on a white background."}
        ]
      }
    ],
    "tool_choice":{"type":"image_generation"},
    "tools":[
      {
        "type":"image_generation",
        "model":"gpt-image-2",
        "size":"1024x1024",
        "output_format":"png",
        "quality":"high",
        "background":"auto"
      }
    ],
    "stream":false
  }'
```

返回体里的图片在：

- `output[].type == "image_generation_call"`
- `output[].result`

## 两张参考图垫图示例

先把本地图片转成 data URL：

```bash
IMG1_B64="$(base64 < /tmp/ref1.png | tr -d '\n')"
IMG2_B64="$(base64 < /tmp/ref2.png | tr -d '\n')"
IMG1_URL="data:image/png;base64,${IMG1_B64}"
IMG2_URL="data:image/png;base64,${IMG2_B64}"
```

然后调用：

```bash
curl -s http://127.0.0.1:8080/openai-routing/v1/responses \
  -H "Authorization: Bearer $OPENAI_ROUTING_UI_API_KEY" \
  -H 'Content-Type: application/json' \
  -d "{
    \"model\":\"gpt-5.4-mini\",
    \"input\":[
      {
        \"type\":\"message\",
        \"role\":\"user\",
        \"content\":[
          {\"type\":\"input_text\",\"text\":\"Use the first image as composition reference and the second image as color/style reference. Generate a polished final image.\"},
          {\"type\":\"input_image\",\"image_url\":\"${IMG1_URL}\"},
          {\"type\":\"input_image\",\"image_url\":\"${IMG2_URL}\"}
        ]
      }
    ],
    \"tool_choice\":{\"type\":\"image_generation\"},
    \"tools\":[
      {
        \"type\":\"image_generation\",
        \"model\":\"gpt-image-2\",
        \"size\":\"1024x1024\",
        \"output_format\":\"png\",
        \"quality\":\"high\",
        \"background\":\"auto\"
      }
    ],
    \"stream\":false
  }"
```

## 直接保存为图片文件

项目内已经提供 demo 脚本：

- [openai_responses_image_demo.py](/Users/joey/repos/my-project/sub2api/tools/openai_responses_image_demo.py)

### 单图文本生图

```bash
python3 tools/openai_responses_image_demo.py \
  --api-key "$OPENAI_ROUTING_UI_API_KEY" \
  --prompt "A minimal red square centered on a white background." \
  --model "gpt-5.4-mini" \
  --image-model "gpt-image-2" \
  --size "1024x1024" \
  --output-format "png" \
  --quality "high" \
  --background "auto" \
  --response-file /tmp/openai-responses-image.json \
  --output-file /tmp/openai-responses-image.png
```

### 两张参考图垫图并直接落盘

```bash
python3 tools/openai_responses_image_demo.py \
  --api-key "$OPENAI_ROUTING_UI_API_KEY" \
  --prompt "Use the first image as composition reference and the second image as color/style reference. Generate a polished final image." \
  --model "gpt-5.4-mini" \
  --image-model "gpt-image-2" \
  --size "1024x1024" \
  --output-format "webp" \
  --quality "high" \
  --background "transparent" \
  --ref /tmp/ref1.png \
  --ref /tmp/ref2.png \
  --response-file /tmp/openai-responses-image-refs.json \
  --output-file /tmp/openai-responses-image-refs.webp
```

## Make 方式

当前分支的 Make target 已统一走 `/v1/responses`：

```bash
OPENAI_ROUTING_UI_API_KEY='<your-sub2api-ui-key>' make openai-routing-image-test
```

可选环境变量：

- `OPENAI_ROUTING_IMAGE_PROMPT`
- `OPENAI_ROUTING_RESPONSES_MODEL`
- `OPENAI_ROUTING_IMAGE_MODEL`
- `OPENAI_ROUTING_IMAGE_SIZE`
- `OPENAI_ROUTING_IMAGE_OUTPUT_FORMAT`
- `OPENAI_ROUTING_IMAGE_QUALITY`
- `OPENAI_ROUTING_IMAGE_BACKGROUND`
- `OPENAI_ROUTING_IMAGE_OUTPUT_FILE`
- `OPENAI_ROUTING_IMAGE_RESPONSE_FILE`
- `OPENAI_ROUTING_IMAGE_REF_1`
- `OPENAI_ROUTING_IMAGE_REF_2`

例如：

```bash
OPENAI_ROUTING_UI_API_KEY='<your-sub2api-ui-key>' \
OPENAI_ROUTING_IMAGE_PROMPT='Use the first image as composition reference and the second image as color/style reference.' \
OPENAI_ROUTING_IMAGE_OUTPUT_FORMAT=webp \
OPENAI_ROUTING_IMAGE_BACKGROUND=transparent \
OPENAI_ROUTING_IMAGE_REF_1=/tmp/ref1.png \
OPENAI_ROUTING_IMAGE_REF_2=/tmp/ref2.png \
OPENAI_ROUTING_IMAGE_OUTPUT_FILE=/tmp/openai-routing-two-ref.webp \
make openai-routing-image-test
```

## 流式返回

如果你设置 `stream=true`，当前分支会返回图片生成流事件：

- `image_generation.partial_image`
- `image_generation.completed`

这两个事件的区别：

- `image_generation.partial_image`
  - 表示阶段性图片
  - 常见字段：
    - `type`
    - `created_at`
    - `partial_image_index`
    - `b64_json`
    - `url`（当 `output_format=url` 时）
    - `background`
    - `output_format`
    - `quality`
    - `size`
- `image_generation.completed`
  - 表示最终图片
  - 常见字段：
    - `type`
    - `created_at`
    - `b64_json`
    - `url`（当 `output_format=url` 时）
    - `background`
    - `output_format`
    - `quality`
    - `size`
    - `usage`

推荐用法：

- 要“尽快看到结果”：
  - 开 `stream=true`
  - 再配合 `partial_images`
- 要“最简单、最稳的脚本保存图片”：
  - 用 `stream=false`

注意：

- `stream=true` 常常会改善“体感等待时间”，因为你能先拿到 partial image
- 但总生成耗时不一定变短；它主要改善的是“先看到东西”，不是“总耗时一定更少”
- 图生图、两张参考图垫图、复杂 prompt 往往明显比纯文本生图慢

示例：流式 + partial image

```bash
curl -N http://127.0.0.1:8080/openai-routing/v1/responses \
  -H "Authorization: Bearer $OPENAI_ROUTING_UI_API_KEY" \
  -H 'Content-Type: application/json' \
  -d '{
    "model":"gpt-5.4-mini",
    "input":[
      {
        "type":"message",
        "role":"user",
        "content":[
          {"type":"input_text","text":"A minimal red square centered on a white background."}
        ]
      }
    ],
    "tool_choice":{"type":"image_generation"},
    "tools":[
      {
        "type":"image_generation",
        "model":"gpt-image-2",
        "size":"1024x1024",
        "output_format":"png",
        "quality":"high",
        "background":"auto",
        "partial_images":2
      }
    ],
    "stream":true
  }'
```

适合前端渐进展示；如果你只是 shell 落盘，默认还是建议用非流式。

## 当前建议

统一建议：

- 文本模型：`gpt-5.4-mini`
- 图片模型：`gpt-image-2`
- 纯文本生图、参考图垫图、保存图片文件，全部走 `/openai-routing/v1/responses`
