use super::*;

#[test]
fn versioned_conversation_preserves_ordered_tool_results() {
    let environment =
        environment(include_str!("../../templates/gemma4-code-v1.jinja").into()).unwrap();
    let cases: Vec<Value> =
        serde_json::from_str(include_str!("../../templates/gemma4-code-v1.cases.json")).unwrap();
    for mut case in cases {
        case["bos_token"] = Value::String("<bos>".into());
        let actual = environment.get_template("chat").unwrap().render(&case);
        if case["rejected"] == true {
            assert!(actual.is_err(), "{}", case["name"]);
        } else {
            assert_eq!(
                actual.unwrap(),
                case["expected"].as_str().unwrap(),
                "{}",
                case["name"]
            );
        }
    }
}

#[test]
fn checkpoint_uses_only_a_matching_rendered_system_prefix() {
    use tokenizers::{models::wordlevel::WordLevel, pre_tokenizers::whitespace::WhitespaceSplit};
    let model = WordLevel::builder()
        .vocab(
            [
                ("?".into(), 0),
                ("bos".into(), 2),
                ("system".into(), 3),
                ("user".into(), 4),
                ("assistant".into(), 5),
            ]
            .into_iter()
            .collect(),
        )
        .unk_token("?".into())
        .build()
        .unwrap();
    let mut tokenizer = Tokenizer::new(model);
    tokenizer.with_pre_tokenizer(Some(WhitespaceSplit));
    let mut renderer = Renderer { tokenizer, bos:"bos".into(), conversation: ConversationFormat::Canonical, environment: environment("{{ bos_token }} {% for m in messages %}{{ m.content }} {% endfor %}{% if add_generation_prompt %}assistant{% endif %}".into()).unwrap() };
    let request = RenderRequest {
        messages: vec![
            serde_json::json!({"role":"system","content":"system"}),
            serde_json::json!({"role":"user","content":"user"}),
        ],
        tools: vec![],
    };
    let measured = renderer.measure(&request).unwrap();
    assert_eq!(measured.input_tokens, 4);
    assert_eq!(measured.rendered.prompt_tokens, [2, 3, 4, 5]);
    let oversized = RenderRequest {
        messages: vec![serde_json::json!({"role":"user","content":"user ".repeat(65536)})],
        tools: vec![],
    };
    let measured = renderer.measure(&oversized).unwrap();
    assert_eq!(measured.input_tokens, 65538);
    assert!(measured.rendered.prompt_tokens.is_empty());
    assert_eq!(measured.rendered.checkpoint_tokens, 0);
    assert!(matches!(
        renderer.render(&oversized, 65536),
        Err(Error::Capacity(_))
    ));
    let rendered = renderer.render(&request, 100).unwrap();
    assert_eq!(rendered.prompt_tokens, [2, 3, 4, 5]);
    assert_eq!(rendered.checkpoint_tokens, 2);
    let large = RenderRequest {
        messages: vec![
            serde_json::json!({"role":"system","content":"system ".repeat(8192)}),
            serde_json::json!({"role":"user","content":"user"}),
        ],
        tools: vec![],
    };
    let rendered = renderer.render(&large, 16384).unwrap();
    assert_eq!(rendered.checkpoint_tokens, 8193);
    assert_eq!(rendered.prompt_tokens.len(), 8195);
    renderer.environment = environment(
        "{% if add_generation_prompt %}bos system user assistant{% else %}assistant{% endif %}"
            .into(),
    )
    .unwrap();
    assert_eq!(renderer.render(&request, 100).unwrap().checkpoint_tokens, 0);
}

#[test]
fn template_supports_python_methods_namespaces_and_nested_arguments() {
    let environment = environment("{%- set ns = namespace(s='') -%}{%- for key, value in args | dictsort -%}{%- set ns.s = ns.s + key + ':' + value.get('text').strip() + ';' -%}{%- endfor -%}{{ ns.s }}".into()).unwrap();
    let output = environment
        .get_template("chat")
        .unwrap()
        .render(serde_json::json!({
            "args": {"z": {"text": " é "}, "a": {"text": " hello "}}
        }))
        .unwrap();
    assert_eq!(output, "a:hello;z:é;");
}

#[test]
fn rendering_limits_output_and_template_work() {
    let mut output = BoundedText(vec![0; MAX_TEXT_BYTES - 1]);
    assert!(output.write_all(b"xx").is_err());
    assert_eq!(output.0.len(), MAX_TEXT_BYTES - 1);
    let mut environment =
        environment("{% for i in range(1000) %}{{ i }}{% endfor %}".into()).unwrap();
    environment.set_fuel(Some(10));
    assert!(
        environment
            .get_template("chat")
            .unwrap()
            .render(())
            .is_err()
    );
}

#[test]
fn template_rejection_is_an_error() {
    let environment = environment("{{ raise_exception('invalid arguments') }}".into()).unwrap();
    assert!(
        environment
            .get_template("chat")
            .unwrap()
            .render(())
            .is_err()
    );
}
