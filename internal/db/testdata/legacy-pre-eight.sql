-- Historical Ent schema fixture; contains no user data or key.
-- Source: 49d5f878ebfe0bc9b1a7313cbf19b9623697d73a:internal/ent/migrate/schema.go
-- Source SHA-256: 4ae005be63a7e4dccefbe1d4ba3cd734db3eb0251cda0fd46cdf5a2db1a3ae59
-- Generated with entgo.io/ent v0.14.6, native SQLCipher 4, WithForeignKeys(false).
-- DDL SHA-256 (from CREATE TABLE through EOF): bd52bb4be44dcf1d48a8ef255b1c4a4a5b327f5c5515f9eb2579ad708006a55d
CREATE TABLE `kaguya_chat_block` (`id` text NOT NULL, `created_at` datetime NOT NULL, `updated_at` datetime NOT NULL, `deleted_at` datetime NULL, `sequence` integer NOT NULL, `type` text NOT NULL, `text` text NOT NULL DEFAULT (''), `tool_call_id` text NOT NULL DEFAULT (''), `tool_name` text NOT NULL DEFAULT (''), `input` text NOT NULL DEFAULT (''), `output` json NULL, `provider_executed` bool NOT NULL DEFAULT (false), `is_error` bool NOT NULL DEFAULT (false), `error_message` text NOT NULL DEFAULT (''), `started_at` datetime NOT NULL, `finished_at` datetime NOT NULL, `start_order` integer NOT NULL, `end_order` integer NOT NULL, `turn_id` text NOT NULL, PRIMARY KEY (`id`));
CREATE TABLE `kaguya_chat_turn` (`id` text NOT NULL, `created_at` datetime NOT NULL, `updated_at` datetime NOT NULL, `deleted_at` datetime NULL, `turn_index` integer NOT NULL, `user_content` text NOT NULL, `provider_id` text NOT NULL, `provider_name` text NOT NULL, `model_id` text NOT NULL, `model_name` text NOT NULL, `api_protocol` text NOT NULL, `status` text NOT NULL DEFAULT ('completed'), `started_at` datetime NOT NULL, `finished_at` datetime NOT NULL, `duration_ms` integer NOT NULL, `tool_calls` integer NOT NULL, `finish_reason` text NOT NULL, `input_tokens` integer NOT NULL, `output_tokens` integer NOT NULL, `total_tokens` integer NOT NULL, `cached_tokens` integer NOT NULL, `reasoning_tokens` integer NOT NULL, `context_tokens` integer NULL, `context_window` integer NOT NULL DEFAULT (0), `context_messages` json NULL, `compaction_count` integer NOT NULL DEFAULT (0), `memory_refs` json NULL, `messages` json NOT NULL, `conversation_id` text NOT NULL, PRIMARY KEY (`id`));
CREATE TABLE `kaguya_conversation` (`id` text NOT NULL, `created_at` datetime NOT NULL, `updated_at` datetime NOT NULL, `deleted_at` datetime NULL, `title` text NOT NULL, `agent_instructions` text NULL, `favorite` bool NOT NULL DEFAULT (false), `memory_mode` text NOT NULL DEFAULT ('inherit'), `turn_count` integer NOT NULL DEFAULT (0), `last_message_at` datetime NOT NULL, `model_id` text NOT NULL, `model_name` text NOT NULL, `duration_ms` integer NOT NULL DEFAULT (0), `tool_calls` integer NOT NULL DEFAULT (0), `input_tokens` integer NOT NULL DEFAULT (0), `output_tokens` integer NOT NULL DEFAULT (0), `total_tokens` integer NOT NULL DEFAULT (0), `cached_tokens` integer NOT NULL DEFAULT (0), `reasoning_tokens` integer NOT NULL DEFAULT (0), `project_id` text NULL, PRIMARY KEY (`id`));
CREATE TABLE `kaguya_mcp_server` (`id` text NOT NULL, `created_at` datetime NOT NULL, `updated_at` datetime NOT NULL, `deleted_at` datetime NULL, `name` text NOT NULL, `transport` text NOT NULL, `command` text NOT NULL DEFAULT (''), `args` json NOT NULL, `env` json NOT NULL, `working_directory` text NOT NULL DEFAULT (''), `url` text NOT NULL DEFAULT (''), `headers` json NOT NULL, `timeout_seconds` integer NOT NULL DEFAULT (60), `enabled` bool NOT NULL DEFAULT (false), PRIMARY KEY (`id`));
CREATE TABLE `kaguya_memory_attempt` (`id` text NOT NULL, `created_at` datetime NOT NULL, `updated_at` datetime NOT NULL, `deleted_at` datetime NULL, `job_id` text NOT NULL, `attempt` integer NOT NULL, `phase` text NOT NULL DEFAULT ('extract'), `model_record_id` text NOT NULL DEFAULT (''), `provider_id` text NOT NULL DEFAULT (''), `upstream_model_id` text NOT NULL DEFAULT (''), `usage_known` bool NOT NULL DEFAULT (false), `input_tokens` integer NOT NULL DEFAULT (0), `output_tokens` integer NOT NULL DEFAULT (0), `total_tokens` integer NOT NULL DEFAULT (0), `cached_tokens` integer NOT NULL DEFAULT (0), `reasoning_tokens` integer NOT NULL DEFAULT (0), `duration_ms` integer NOT NULL DEFAULT (0), `result_code` text NOT NULL DEFAULT (''), PRIMARY KEY (`id`));
CREATE TABLE `kaguya_memory_evidence` (`id` text NOT NULL, `created_at` datetime NOT NULL, `updated_at` datetime NOT NULL, `deleted_at` datetime NULL, `revision_id` text NOT NULL, `claim_key` text NOT NULL, `source_id` text NOT NULL, `part_key` text NOT NULL, `quote` text NOT NULL, `quote_hash` text NOT NULL DEFAULT (''), `relation` text NOT NULL DEFAULT ('support'), `basis` text NOT NULL DEFAULT ('synthesis'), PRIMARY KEY (`id`));
CREATE TABLE `kaguya_memory_job` (`id` text NOT NULL, `created_at` datetime NOT NULL, `updated_at` datetime NOT NULL, `deleted_at` datetime NULL, `kind` text NOT NULL DEFAULT ('compile'), `scope_key` text NOT NULL, `conversation_id` text NOT NULL DEFAULT (''), `input_source_ids` json NULL, `input_hash` text NOT NULL DEFAULT (''), `source_budget` integer NOT NULL DEFAULT (0), `compiler_version` text NOT NULL DEFAULT (''), `status` text NOT NULL DEFAULT ('pending'), `attempt` integer NOT NULL DEFAULT (0), `lease_token` text NOT NULL DEFAULT (''), `lease_expires_at` datetime NULL, `next_attempt_at` datetime NULL, `error_code` text NOT NULL DEFAULT (''), `error_summary` text NOT NULL DEFAULT (''), `result_json` text NOT NULL DEFAULT (''), `policy_epoch` integer NOT NULL DEFAULT (0), `started_at` datetime NULL, `finished_at` datetime NULL, PRIMARY KEY (`id`));
CREATE TABLE `kaguya_memory_link` (`id` text NOT NULL, `created_at` datetime NOT NULL, `updated_at` datetime NOT NULL, `deleted_at` datetime NULL, `from_page_id` text NOT NULL, `to_page_id` text NOT NULL, `relation` text NOT NULL DEFAULT ('related'), PRIMARY KEY (`id`));
CREATE TABLE `kaguya_memory_page` (`id` text NOT NULL, `created_at` datetime NOT NULL, `updated_at` datetime NOT NULL, `deleted_at` datetime NULL, `scope_key` text NOT NULL, `canonical_key` text NOT NULL, `kind` text NOT NULL, `title` text NOT NULL, `summary` text NOT NULL DEFAULT (''), `body` text NOT NULL, `aliases` json NULL, `status` text NOT NULL DEFAULT ('proposed'), `version` integer NOT NULL DEFAULT (1), `pinned` bool NOT NULL DEFAULT (false), `user_locked` bool NOT NULL DEFAULT (false), `expires_at` datetime NULL, PRIMARY KEY (`id`));
CREATE TABLE `kaguya_memory_revision` (`id` text NOT NULL, `created_at` datetime NOT NULL, `updated_at` datetime NOT NULL, `deleted_at` datetime NULL, `page_id` text NOT NULL, `version` integer NOT NULL, `canonical_key` text NOT NULL, `kind` text NOT NULL, `title` text NOT NULL, `summary` text NOT NULL DEFAULT (''), `body` text NOT NULL, `aliases` json NULL, `status` text NOT NULL, `pinned` bool NOT NULL DEFAULT (false), `user_locked` bool NOT NULL DEFAULT (false), `expires_at` datetime NULL, `claims` json NULL, `actor` text NOT NULL, `job_id` text NOT NULL DEFAULT (''), `reason` text NOT NULL DEFAULT (''), PRIMARY KEY (`id`));
CREATE TABLE `kaguya_memory_search_doc` (`id` integer NOT NULL PRIMARY KEY AUTOINCREMENT, `page_id` text NOT NULL, `page_version` integer NOT NULL, `normalizer_version` integer NOT NULL, `title_terms` text NOT NULL DEFAULT (''), `alias_terms` text NOT NULL DEFAULT (''), `summary_terms` text NOT NULL DEFAULT (''), `body_terms` text NOT NULL DEFAULT (''));
CREATE TABLE `kaguya_memory_source` (`id` text NOT NULL, `created_at` datetime NOT NULL, `updated_at` datetime NOT NULL, `deleted_at` datetime NULL, `source_key` text NOT NULL, `kind` text NOT NULL DEFAULT ('turn'), `scope_key` text NOT NULL, `conversation_id` text NOT NULL DEFAULT (''), `turn_id` text NOT NULL DEFAULT (''), `projection_version` integer NOT NULL DEFAULT (1), `cursor_part` integer NOT NULL DEFAULT (0), `content_hash` text NOT NULL DEFAULT (''), `raw_content` text NOT NULL DEFAULT (''), `document_path` text NOT NULL DEFAULT (''), `state` text NOT NULL DEFAULT ('pending'), `job_id` text NOT NULL DEFAULT (''), `captured_at` datetime NOT NULL, `policy_epoch` integer NOT NULL DEFAULT (0), PRIMARY KEY (`id`));
CREATE TABLE `kaguya_models_info` (`id` text NOT NULL, `created_at` datetime NOT NULL, `updated_at` datetime NOT NULL, `deleted_at` datetime NULL, `model_name` text NOT NULL, `model_id` text NOT NULL, `api_protocol` text NULL DEFAULT ('openai-chat'), `request_path` text NOT NULL DEFAULT (''), `reasoning_enabled` integer NULL DEFAULT (1), `reasoning_effort` text NULL DEFAULT ('medium'), `token_context_window` integer NULL, `token_max_output_tokens` integer NULL, `capability_tool_use` integer NULL DEFAULT (1), `capability_vision` integer NULL DEFAULT (1), `capability_structured_output` integer NULL DEFAULT (1), `provider_id` text NOT NULL, PRIMARY KEY (`id`));
CREATE TABLE `kaguya_project` (`id` text NOT NULL, `created_at` datetime NOT NULL, `updated_at` datetime NOT NULL, `deleted_at` datetime NULL, `name` text NOT NULL, `path` text NOT NULL, `description` text NOT NULL DEFAULT (''), PRIMARY KEY (`id`));
CREATE TABLE `kaguya_provider_info` (`id` text NOT NULL, `created_at` datetime NOT NULL, `updated_at` datetime NOT NULL, `deleted_at` datetime NULL, `provider_name` text NOT NULL, `provider_type` text NOT NULL DEFAULT ('normal'), `api_key` text NULL, `base_url` text NULL, PRIMARY KEY (`id`));
CREATE TABLE `kaguya_system_info` (`id` text NOT NULL, `created_at` datetime NOT NULL, `updated_at` datetime NOT NULL, `deleted_at` datetime NULL, `agent_max_steps` integer NOT NULL DEFAULT (0), `context_compaction_percent` integer NOT NULL DEFAULT (90), `command_timeout_seconds` integer NOT NULL DEFAULT (120), `chat_max_retries` integer NOT NULL DEFAULT (5), `global_agents_paths` json NULL, `global_system_prompt` text NOT NULL DEFAULT ('You are **Kaguya**, a calm, rational, and professional intelligent assistant.

Your role is to understand the user''s needs, analyze problems, and provide accurate, clear, and useful responses.

Follow these principles:

* Be concise, accurate, and direct. Prioritize the conclusion.
* Do not fabricate facts, results, or unknown information.
* When information is insufficient, state it clearly instead of making unsupported assumptions.
* Maintain independent judgment and do not agree with incorrect claims simply to please the user.
* Prefer simple, reliable, and actionable suggestions.
* Communicate in Chinese by default. Keep code, commands, API names, configuration keys, and error messages in their original form when appropriate.

Chat display capabilities:
* The chat renders Markdown and fenced code blocks labeled mermaid as diagrams. For architecture, flow, and sequence diagrams, include the complete Mermaid code in your answer in the same turn, unless the user requests another format.
* MCP tools execute on the server. This chat does not host MCP Apps or Excalidraw widgets. A tool message such as "Diagram displayed" or a checkpoint ID does not mean the user can see a diagram here. Include a Mermaid diagram in the answer when appropriate; never claim a widget is visible based only on a tool''s success message.

Your overall personality is **calm, perceptive, restrained, and reliable, with a subtle sense of non-human intelligence without excessive role-playing.**
'), `system_prompt` text NOT NULL DEFAULT (''), `model_sync_enabled` bool NOT NULL DEFAULT (false), `provider_sync_url` text NOT NULL DEFAULT ('https://models.dev/api.json'), `model_sync_url` text NOT NULL DEFAULT ('https://models.dev/models.json'), `model_sync_interval_hours` integer NOT NULL DEFAULT (24), `model_catalog_json` text NOT NULL DEFAULT ('[]'), `model_catalog_count` integer NOT NULL DEFAULT (0), `provider_catalog_json` text NOT NULL DEFAULT ('[]'), `provider_catalog_count` integer NOT NULL DEFAULT (0), `model_sync_last_attempt_at` datetime NULL, `model_sync_last_success_at` datetime NULL, `model_sync_last_error` text NOT NULL DEFAULT (''), `default_model_id` text NOT NULL DEFAULT (''), `task_model_id` text NOT NULL DEFAULT (''), `memory_enabled` bool NOT NULL DEFAULT (true), `memory_auto_capture` bool NOT NULL DEFAULT (true), `memory_context_tokens` integer NOT NULL DEFAULT (2000), `memory_policy_epoch` integer NOT NULL DEFAULT (0), PRIMARY KEY (`id`));
CREATE TABLE `kaguya_task_usage` (`id` text NOT NULL, `created_at` datetime NOT NULL, `updated_at` datetime NOT NULL, `deleted_at` datetime NULL, `kind` text NOT NULL, `conversation_id` text NOT NULL DEFAULT (''), `provider_id` text NOT NULL DEFAULT (''), `provider_name` text NOT NULL DEFAULT (''), `model_id` text NOT NULL DEFAULT (''), `model_name` text NOT NULL DEFAULT (''), `finished_at` datetime NOT NULL, `usage_known` bool NOT NULL DEFAULT (false), `input_tokens` integer NOT NULL DEFAULT (0), `output_tokens` integer NOT NULL DEFAULT (0), `reasoning_tokens` integer NOT NULL DEFAULT (0), `cached_tokens` integer NOT NULL DEFAULT (0), `total_tokens` integer NOT NULL DEFAULT (0), PRIMARY KEY (`id`));
CREATE UNIQUE INDEX `kaguya_memory_search_doc_page_id_key` ON `kaguya_memory_search_doc` (`page_id`);
CREATE INDEX `kaguyachatblock_created_at` ON `kaguya_chat_block` (`created_at`);
CREATE INDEX `kaguyachatblock_deleted_at` ON `kaguya_chat_block` (`deleted_at`);
CREATE INDEX `kaguyachatblock_id` ON `kaguya_chat_block` (`id`);
CREATE UNIQUE INDEX `kaguyachatblock_turn_id_sequence` ON `kaguya_chat_block` (`turn_id`, `sequence`);
CREATE INDEX `kaguyachatblock_updated_at` ON `kaguya_chat_block` (`updated_at`);
CREATE UNIQUE INDEX `kaguyachatturn_conversation_id_turn_index` ON `kaguya_chat_turn` (`conversation_id`, `turn_index`);
CREATE INDEX `kaguyachatturn_created_at` ON `kaguya_chat_turn` (`created_at`);
CREATE INDEX `kaguyachatturn_deleted_at` ON `kaguya_chat_turn` (`deleted_at`);
CREATE INDEX `kaguyachatturn_finished_at` ON `kaguya_chat_turn` (`finished_at`);
CREATE INDEX `kaguyachatturn_id` ON `kaguya_chat_turn` (`id`);
CREATE INDEX `kaguyachatturn_updated_at` ON `kaguya_chat_turn` (`updated_at`);
CREATE INDEX `kaguyaconversation_created_at` ON `kaguya_conversation` (`created_at`);
CREATE INDEX `kaguyaconversation_deleted_at` ON `kaguya_conversation` (`deleted_at`);
CREATE INDEX `kaguyaconversation_deleted_at_favorite_last_message_at_id` ON `kaguya_conversation` (`deleted_at`, `favorite`, `last_message_at`, `id`);
CREATE INDEX `kaguyaconversation_deleted_at_last_message_at_id` ON `kaguya_conversation` (`deleted_at`, `last_message_at`, `id`);
CREATE INDEX `kaguyaconversation_deleted_at_title` ON `kaguya_conversation` (`deleted_at`, `title`);
CREATE INDEX `kaguyaconversation_id` ON `kaguya_conversation` (`id`);
CREATE INDEX `kaguyaconversation_project_id_deleted_at_last_message_at_id` ON `kaguya_conversation` (`project_id`, `deleted_at`, `last_message_at`, `id`);
CREATE INDEX `kaguyaconversation_updated_at` ON `kaguya_conversation` (`updated_at`);
CREATE INDEX `kaguyamcpserver_created_at` ON `kaguya_mcp_server` (`created_at`);
CREATE INDEX `kaguyamcpserver_deleted_at` ON `kaguya_mcp_server` (`deleted_at`);
CREATE INDEX `kaguyamcpserver_id` ON `kaguya_mcp_server` (`id`);
CREATE UNIQUE INDEX `kaguyamcpserver_name` ON `kaguya_mcp_server` (`name`) WHERE deleted_at IS NULL;
CREATE INDEX `kaguyamcpserver_updated_at` ON `kaguya_mcp_server` (`updated_at`);
CREATE INDEX `kaguyamemoryattempt_created_at` ON `kaguya_memory_attempt` (`created_at`);
CREATE INDEX `kaguyamemoryattempt_deleted_at` ON `kaguya_memory_attempt` (`deleted_at`);
CREATE INDEX `kaguyamemoryattempt_id` ON `kaguya_memory_attempt` (`id`);
CREATE INDEX `kaguyamemoryattempt_job_id_attempt` ON `kaguya_memory_attempt` (`job_id`, `attempt`);
CREATE INDEX `kaguyamemoryattempt_updated_at` ON `kaguya_memory_attempt` (`updated_at`);
CREATE INDEX `kaguyamemoryevidence_created_at` ON `kaguya_memory_evidence` (`created_at`);
CREATE INDEX `kaguyamemoryevidence_deleted_at` ON `kaguya_memory_evidence` (`deleted_at`);
CREATE INDEX `kaguyamemoryevidence_id` ON `kaguya_memory_evidence` (`id`);
CREATE INDEX `kaguyamemoryevidence_revision_id_claim_key` ON `kaguya_memory_evidence` (`revision_id`, `claim_key`);
CREATE INDEX `kaguyamemoryevidence_source_id` ON `kaguya_memory_evidence` (`source_id`);
CREATE INDEX `kaguyamemoryevidence_updated_at` ON `kaguya_memory_evidence` (`updated_at`);
CREATE INDEX `kaguyamemoryjob_conversation_id_scope_key` ON `kaguya_memory_job` (`conversation_id`, `scope_key`);
CREATE INDEX `kaguyamemoryjob_created_at` ON `kaguya_memory_job` (`created_at`);
CREATE INDEX `kaguyamemoryjob_deleted_at` ON `kaguya_memory_job` (`deleted_at`);
CREATE INDEX `kaguyamemoryjob_id` ON `kaguya_memory_job` (`id`);
CREATE INDEX `kaguyamemoryjob_lease_expires_at` ON `kaguya_memory_job` (`lease_expires_at`);
CREATE INDEX `kaguyamemoryjob_status_next_attempt_at` ON `kaguya_memory_job` (`status`, `next_attempt_at`);
CREATE INDEX `kaguyamemoryjob_updated_at` ON `kaguya_memory_job` (`updated_at`);
CREATE INDEX `kaguyamemorylink_created_at` ON `kaguya_memory_link` (`created_at`);
CREATE INDEX `kaguyamemorylink_deleted_at` ON `kaguya_memory_link` (`deleted_at`);
CREATE UNIQUE INDEX `kaguyamemorylink_from_page_id_to_page_id_relation` ON `kaguya_memory_link` (`from_page_id`, `to_page_id`, `relation`);
CREATE INDEX `kaguyamemorylink_id` ON `kaguya_memory_link` (`id`);
CREATE INDEX `kaguyamemorylink_to_page_id` ON `kaguya_memory_link` (`to_page_id`);
CREATE INDEX `kaguyamemorylink_updated_at` ON `kaguya_memory_link` (`updated_at`);
CREATE INDEX `kaguyamemorypage_created_at` ON `kaguya_memory_page` (`created_at`);
CREATE INDEX `kaguyamemorypage_deleted_at` ON `kaguya_memory_page` (`deleted_at`);
CREATE INDEX `kaguyamemorypage_id` ON `kaguya_memory_page` (`id`);
CREATE UNIQUE INDEX `kaguyamemorypage_scope_key_canonical_key` ON `kaguya_memory_page` (`scope_key`, `canonical_key`);
CREATE INDEX `kaguyamemorypage_scope_key_status_updated_at` ON `kaguya_memory_page` (`scope_key`, `status`, `updated_at`);
CREATE INDEX `kaguyamemorypage_updated_at` ON `kaguya_memory_page` (`updated_at`);
CREATE INDEX `kaguyamemoryrevision_created_at` ON `kaguya_memory_revision` (`created_at`);
CREATE INDEX `kaguyamemoryrevision_deleted_at` ON `kaguya_memory_revision` (`deleted_at`);
CREATE INDEX `kaguyamemoryrevision_id` ON `kaguya_memory_revision` (`id`);
CREATE UNIQUE INDEX `kaguyamemoryrevision_page_id_version` ON `kaguya_memory_revision` (`page_id`, `version`);
CREATE INDEX `kaguyamemoryrevision_updated_at` ON `kaguya_memory_revision` (`updated_at`);
CREATE INDEX `kaguyamemorysearchdoc_page_version` ON `kaguya_memory_search_doc` (`page_version`);
CREATE INDEX `kaguyamemorysource_conversation_id` ON `kaguya_memory_source` (`conversation_id`);
CREATE INDEX `kaguyamemorysource_created_at` ON `kaguya_memory_source` (`created_at`);
CREATE INDEX `kaguyamemorysource_deleted_at` ON `kaguya_memory_source` (`deleted_at`);
CREATE INDEX `kaguyamemorysource_id` ON `kaguya_memory_source` (`id`);
CREATE INDEX `kaguyamemorysource_job_id` ON `kaguya_memory_source` (`job_id`);
CREATE UNIQUE INDEX `kaguyamemorysource_source_key` ON `kaguya_memory_source` (`source_key`);
CREATE INDEX `kaguyamemorysource_state_captured_at` ON `kaguya_memory_source` (`state`, `captured_at`);
CREATE INDEX `kaguyamemorysource_updated_at` ON `kaguya_memory_source` (`updated_at`);
CREATE INDEX `kaguyamodelsinfo_created_at` ON `kaguya_models_info` (`created_at`);
CREATE INDEX `kaguyamodelsinfo_deleted_at` ON `kaguya_models_info` (`deleted_at`);
CREATE INDEX `kaguyamodelsinfo_id` ON `kaguya_models_info` (`id`);
CREATE INDEX `kaguyamodelsinfo_model_id` ON `kaguya_models_info` (`model_id`);
CREATE INDEX `kaguyamodelsinfo_provider_id` ON `kaguya_models_info` (`provider_id`);
CREATE UNIQUE INDEX `kaguyamodelsinfo_provider_id_model_id` ON `kaguya_models_info` (`provider_id`, `model_id`) WHERE deleted_at IS NULL;
CREATE INDEX `kaguyamodelsinfo_updated_at` ON `kaguya_models_info` (`updated_at`);
CREATE INDEX `kaguyaproject_created_at` ON `kaguya_project` (`created_at`);
CREATE INDEX `kaguyaproject_deleted_at` ON `kaguya_project` (`deleted_at`);
CREATE INDEX `kaguyaproject_deleted_at_name_id` ON `kaguya_project` (`deleted_at`, `name`, `id`);
CREATE INDEX `kaguyaproject_id` ON `kaguya_project` (`id`);
CREATE UNIQUE INDEX `kaguyaproject_path` ON `kaguya_project` (`path`) WHERE deleted_at IS NULL;
CREATE INDEX `kaguyaproject_updated_at` ON `kaguya_project` (`updated_at`);
CREATE INDEX `kaguyaproviderinfo_created_at` ON `kaguya_provider_info` (`created_at`);
CREATE INDEX `kaguyaproviderinfo_deleted_at` ON `kaguya_provider_info` (`deleted_at`);
CREATE INDEX `kaguyaproviderinfo_id` ON `kaguya_provider_info` (`id`);
CREATE UNIQUE INDEX `kaguyaproviderinfo_provider_name` ON `kaguya_provider_info` (`provider_name`) WHERE deleted_at IS NULL;
CREATE INDEX `kaguyaproviderinfo_updated_at` ON `kaguya_provider_info` (`updated_at`);
CREATE INDEX `kaguyasysteminfo_created_at` ON `kaguya_system_info` (`created_at`);
CREATE INDEX `kaguyasysteminfo_deleted_at` ON `kaguya_system_info` (`deleted_at`);
CREATE INDEX `kaguyasysteminfo_id` ON `kaguya_system_info` (`id`);
CREATE INDEX `kaguyasysteminfo_updated_at` ON `kaguya_system_info` (`updated_at`);
CREATE INDEX `kaguyataskusage_created_at` ON `kaguya_task_usage` (`created_at`);
CREATE INDEX `kaguyataskusage_deleted_at` ON `kaguya_task_usage` (`deleted_at`);
CREATE INDEX `kaguyataskusage_finished_at` ON `kaguya_task_usage` (`finished_at`);
CREATE INDEX `kaguyataskusage_id` ON `kaguya_task_usage` (`id`);
CREATE INDEX `kaguyataskusage_updated_at` ON `kaguya_task_usage` (`updated_at`);
