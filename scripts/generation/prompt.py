"""Render a local chat fixture with the checkpoint's pinned tokenizer and template."""

import json
from pathlib import Path
import sys

from transformers import AutoTokenizer


def main():
    model, messages, output = map(Path, sys.argv[1:])
    fixture = json.loads(messages.read_text())
    tokenizer = AutoTokenizer.from_pretrained(model, local_files_only=True, trust_remote_code=False)
    tokens = tokenizer.apply_chat_template(fixture["messages"], tools=fixture.get("tools"),
                                          tokenize=True, return_dict=False, add_generation_prompt=True, enable_thinking=False)
    output.write_text(json.dumps({"prompt_tokens": tokens}) + "\n")


if __name__ == "__main__":
    main()
