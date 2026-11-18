"""Versioned conversation formatting shared by student training, export and serving."""
import hashlib
import json
from pathlib import Path


PROFILE = "gemma4-code-v1"
TEMPLATE = Path(__file__).resolve().parents[3] / "lycaon/internal/generation/templates/gemma4-code-v1.jinja"
CASES = TEMPLATE.with_name("gemma4-code-v1.cases.json")
# Text model types of every Gemma 4 student tier; the host serves them all in gemma4-code-v1.
GEMMA4_TEXT = {"gemma4_text", "gemma4_unified_text"}


def template_sha256(text):
    """The identity of a chat template: the SHA-256 of its UTF-8 bytes, as a file digest reads it."""
    return hashlib.sha256(text.encode()).hexdigest()


def configure_training_renderer(tokenizer, model):
    config = json.loads((Path(model) / "config.json").read_text())
    if config.get("text_config", config).get("model_type") in GEMMA4_TEXT:
        tokenizer.chat_template = TEMPLATE.read_text()
        tokenizer.init_kwargs["paintedwolf_conversation_format"] = PROFILE
        return PROFILE
    return "checkpoint"
