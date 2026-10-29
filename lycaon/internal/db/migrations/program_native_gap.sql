ALTER TABLE invocation_program_executions ADD COLUMN native_gap_json TEXT NOT NULL DEFAULT '' CHECK (native_gap_json = '' OR json_valid(native_gap_json));
