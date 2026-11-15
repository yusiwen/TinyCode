# Images

A picture can be attached to a message: a screenshot of a failing test, a design
mock, a photograph of an error on a phone screen. It travels with the user
message — into the request, into the history the next step reads, and into the
session file — and each provider backend serializes it as that vendor's own
content block.

## Attaching one

In the TUI, `/image <path>` holds pictures for the **next** message:

```
/image ~/Desktop/failure.png
> 已附加 failure.png。它会随你的下一条消息一起发送。
```

`/image` with no argument lists what is pending, and `/image clear` drops it. The
transcript names each attachment beside the words it was sent with, so a reader of
a saved session can see what the model was shown.

On the command line, `--image` is repeatable and belongs to the prompt:

```console
$ tinycode --image ./mock.png "build this layout"
```

A file that is not an image is refused where it was attached — in the transcript
or before the run starts — with the type the bytes really are, not the one the
extension claims.

## What is accepted

| Rule | Value |
| --- | --- |
| Formats | png, jpeg, gif, webp — sniffed from the bytes, never from the name |
| Size | 4 MiB per image |
| Count | 4 per message |
| Refused on purpose | SVG and PDF: they are documents, not raster images, and the models do not read them as pictures |

The caps are the request body and the model's context, in that order. An image
that passes them is stored base64-encoded in the message, which is what every
provider wants and what makes a resume work.

## What each backend receives

| Backend | Shape |
| --- | --- |
| OpenAI-compatible (`provider_openai.go`) | a content **block array**: a `text` block with the message's words, then one `image_url` block per picture carrying a `data:` URL |
| Anthropic (`provider_anthropic.go`) | an `image` block whose `source` is `{type: base64, media_type, data}` |
| Gemini (`provider_gemini.go`) | an `inlineData` part: `{mimeType, data}` |
| Bedrock (`provider_bedrock.go`) | a Converse `image` block: `{format: png, source: {bytes: <base64>}}` — the format names the type without the `image/` prefix |
| Ollama (`provider_ollama.go`) | the raw base64 strings in `images`, beside `content` |

A message with no images serializes exactly as it did before this existed: the
field is omitted, so a text-only conversation's request body does not change.

## What it costs

The picture is part of the user message, so it is sent again on **every** step of
the run and every later run whose history still contains that turn. One screenshot
is therefore not one image's worth of tokens but one per model call. That is the
same rule as any other message content, and it is worth knowing before attaching
four pictures to a long task.

## What is not covered

- **No paste or drag-and-drop.** A terminal delivers the clipboard as text or not
  at all; attaching is a path. A future clipboard image path would enter through
  the same `types.NewImage` and change nothing downstream.
- **No downscaling.** An image over the cap is refused rather than resized: a
  silent resize changes what the model is looking at, and the code that would do
  it well is not in the standard library.
- **No OCR and no format conversion.**
- **A text-only model still fails at the provider.** The attachment reaches the
  request; a model that does not accept pictures answers with its own error, which
  the run reports. Choosing a vision-capable model is the configuration step.
- **`read_file` does not read images.** It refuses and names `/image` and
  `--image`, because reading the bytes as lines fills the context with mojibake
  that looks like a file with a handful of enormous lines.
