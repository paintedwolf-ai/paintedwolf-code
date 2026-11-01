"""Versioned conversation formatting shared by E4B training and export."""
import json
from pathlib import Path


TEMPLATE = Path(__file__).resolve().parents[3] / "lycaon/internal/generation/templates/gemma4-code-v1.jinja"


def configure_training_renderer(tokenizer, model):
    config = json.loads((Path(model) / "config.json").read_text())
    text = config.get("text_config", config)
    if (text.get("model_type") == "gemma4_text"
            and text.get("hidden_size") == 2560
            and text.get("num_hidden_layers") == 42
            and text.get("num_kv_shared_layers") == 18):
        tokenizer.chat_template = TEMPLATE.read_text()
        tokenizer.init_kwargs["paintedwolf_conversation_format"] = "gemma4-code-v1"
        return "gemma4-code-v1"
    return "checkpoint"


def ordered_messages(messages):
    calls = {}
    for message in messages:
        for call in message.get("tool_calls") or []:
            identity, name = call.get("id"), call.get("function", {}).get("name")
            if not identity or not name or identity in calls:
                raise ValueError("tool history requires unique call identities")
            calls[identity] = name
    result = []
    for message in messages:
        if message["role"] == "tool":
            paired = calls.get(message.get("tool_call_id"))
            name = message.get("name") or paired
            if not name or (paired is not None and name != paired):
                raise ValueError("tool result requires an unambiguous tool identity")
            message = dict(message, name=name)
        result.append(message)
    return result
