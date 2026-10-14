package recall

// historyRecord retains public transcript fields without private provider replay.
const historyRecord = `json_object(
 'id',id,'role',role,'content',content,'origin',origin,'authority',authority,'trust_tier',trust_tier,
 'author_person_id',author_person_id,'kind',kind,
 'content_parts',json_extract(NULLIF(content_parts_json,''),'$'),
 'host_secret_redaction',json_extract(NULLIF(host_secret_redaction_json,''),'$'),
 'artifact_ids',json_extract(NULLIF(artifact_ids_json,''),'$'),
 'tool_calls',(SELECT json_group_array(json_remove(value,'$.extra_content','$.wire_id'))
  FROM json_each(COALESCE(NULLIF(tool_calls_json,''),'[]'))),
 'tool_result',json_extract(NULLIF(tool_result_json,''),'$'),
 'worker_summary',json_extract(NULLIF(worker_summary_json,''),'$'),
 'grounding',json_extract(NULLIF(grounding_json,''),'$'))`
