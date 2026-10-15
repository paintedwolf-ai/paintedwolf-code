package recall

const historyCalls = `(SELECT json_group_array(json_remove(value,'$.extra_content','$.wire_id'))
  FROM json_each(COALESCE(NULLIF(tool_calls_json,''),'[]')))`

// historyRecord retains public transcript fields without private provider replay.
// Metadata and tool envelopes precede large content bodies so the first page
// surfaces call arguments and result metadata before paging recorded bytes.
const historyRecord = `json_object(
 'id',id,'role',role,'origin',origin,'authority',authority,'trust_tier',trust_tier,
 'author_person_id',author_person_id,'kind',kind,
 'host_secret_redaction',json_extract(NULLIF(host_secret_redaction_json,''),'$'),
 'artifact_ids',json_extract(NULLIF(artifact_ids_json,''),'$'),
 'tool_calls',` + historyCalls + `,
 'tool_result',json_extract(json_remove(NULLIF(tool_result_json,''),'$.content','$.invocation'),'$'),
 'worker_summary',json_extract(NULLIF(worker_summary_json,''),'$'),
 'grounding',json_extract(NULLIF(grounding_json,''),'$'),
 'content_parts',json_extract(NULLIF(content_parts_json,''),'$'),
 'content',` + historyText + `)`
