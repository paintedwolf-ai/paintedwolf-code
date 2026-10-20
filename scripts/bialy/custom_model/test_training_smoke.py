"""Random tiny-model smoke test: no downloaded weights or provider calls."""
import json
from pathlib import Path
import tempfile
import unittest

from common import digest
from train import parser, train
from export import export


def fixture(group, mode):
    prompt = [{"role":"system","content":"Be precise"},{"role":"user","content":group}]
    capture = {"schema":"pw-model-request-capture/2","messages":prompt,"tools":[],"controls":{},
               "host_revision":"a"*40,"epoch":"fixture","tool_surface":"legacy","state_sha256":"b"*64,"catalog_sha256":"c"*64}
    capture["sha256"] = digest(capture)
    target = {"role":"assistant","text":"answer","tool_calls":[]}
    validation = {k:capture[k] for k in ("host_revision","epoch","tool_surface","state_sha256","catalog_sha256")}
    validation.update(schema="pw-training-validation/1",validator="paintedwolf-host-training-validation/1",
                      receipt_id="fixture",request_sha256=capture["sha256"],response_sha256=digest(target),
                      outcome="accepted",response_origin="model",
                      quality={"method":"deterministic","verdict":"pass","evidence_ids":["fixture"],
                               "request_sha256":capture["sha256"],"response_sha256":digest(target)})
    row = {"schema":"pw-custom-"+mode+"/1","prompt":prompt,"request_capture":capture,"validation":validation,"split_group":group}
    if mode == "sft":
        row["target"] = target
    else:
        row.update(chosen=target,rejected=dict(target,text="wrong"))
        validation.update(chosen_outcome="accepted",rejected_outcome="rejected",preference="chosen",
                          chosen_sha256=digest(target),rejected_sha256=digest(row["rejected"]))
    return row


class Smoke(unittest.TestCase):
    def test_sft_merge_and_dpo(self):
        from transformers import LlamaConfig, LlamaForCausalLM, PreTrainedTokenizerFast
        from tokenizers import Tokenizer, models, pre_tokenizers
        import torch
        torch.set_num_threads(1)
        with tempfile.TemporaryDirectory() as root:
            root = Path(root)
            model_path = root / "base"
            model_path.mkdir()
            vocab = {c:i+1 for i,c in enumerate(chr(n) for n in range(32,127))}
            vocab["[UNK]"] = 0
            tok = Tokenizer(models.WordLevel(vocab,unk_token="[UNK]"))
            tok.pre_tokenizer = pre_tokenizers.Split("",behavior="isolated")
            tokenizer = PreTrainedTokenizerFast(tokenizer_object=tok,unk_token="[UNK]")
            tokenizer.chat_template = "{{ tools|tojson }}{% for m in messages %}<{{ m.role }}>{{ m.content }}{% endfor %}{% if add_generation_prompt %}<assistant>{% else %}<end>{% endif %}"
            tokenizer.save_pretrained(model_path)
            model = LlamaForCausalLM(LlamaConfig(vocab_size=len(vocab),hidden_size=16,intermediate_size=32,
                       num_hidden_layers=1,num_attention_heads=2,num_key_value_heads=2,max_position_embeddings=256))
            model.save_pretrained(model_path,safe_serialization=True)
            for mode in ("sft","dpo"):
                for group in ("train","validation","holdout"):
                    (root / (group+".jsonl")).write_text(json.dumps(fixture(group,mode))+"\n")
                base = model_path if mode == "sft" else root / "merged/model"
                argv = ["--model",str(base),"--mode",mode,"--train",str(root/"train.jsonl"),
                        "--validation",str(root/"validation.jsonl"),"--holdout",str(root/"holdout.jsonl"),"--out",str(root/mode),"--targets","q_proj,v_proj","--rank","2","--max-length","256","--epochs","2","--checkpoint-every","1"]
                if mode == "dpo":
                    argv += ["--reference",str(base)]
                paused = train(parser().parse_args(argv + ["--max-updates","1"]))
                self.assertEqual(paused["status"], "paused")
                self.assertFalse((root/mode/"result.json").exists())
                train(parser().parse_args(argv + ["--resume"]))
                self.assertTrue((root/mode/"result.json").is_file())
                uninterrupted = list(argv)
                uninterrupted[uninterrupted.index("--out") + 1] = str(root / (mode + "-uninterrupted"))
                train(parser().parse_args(uninterrupted))
                from safetensors.torch import load_file
                resumed_weights = load_file(root/mode/"adapter/adapter_model.safetensors")
                full_weights = load_file(root/(mode+"-uninterrupted")/"adapter/adapter_model.safetensors")
                self.assertEqual(resumed_weights.keys(), full_weights.keys())
                for name in resumed_weights:
                    torch.testing.assert_close(resumed_weights[name], full_weights[name], rtol=0, atol=0)
                if mode == "sft":
                    export(base,root/mode,root/"merged")
                    self.assertTrue((root/"merged/manifest.json").is_file())
                    # Simulate a crash after directory publication, before the final receipt.
                    (root/"merged/manifest.json").unlink()
                    export(base,root/mode,root/"merged",resume=True)
                    export(base,root/mode,root/"merged",resume=True)


if __name__ == "__main__":
    unittest.main()
