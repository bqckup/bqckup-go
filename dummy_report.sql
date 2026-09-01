BEGIN;

DELETE FROM artifacts;
DELETE FROM backup_runs;
DELETE FROM report_deliveries;

INSERT INTO backup_runs (
  id, site_name, status, forced, started_at, finished_at, duration_millis, error_category, error_message, created_at, updated_at
) VALUES
  (
    'run_alpha_1',
    'alpha.example',
    'success',
    0,
    '2026-09-01 05:00:00',
    '2026-09-01 05:20:00',
    1200000,
    '',
    '',
    '2026-09-01 05:20:00',
    '2026-09-01 05:20:00'
  ),
  (
    'run_beta_1',
    'beta.example',
    'failed',
    0,
    '2026-09-01 06:10:00',
    '2026-09-01 06:35:00',
    1500000,
    'storage',
    'timeout while uploading to destination',
    '2026-09-01 06:35:00',
    '2026-09-01 06:35:00'
  ),
  (
    'run_gamma_1',
    'gamma.example',
    'success',
    0,
    '2026-09-01 07:00:00',
    '2026-09-01 07:18:00',
    1080000,
    '',
    '',
    '2026-09-01 07:18:00',
    '2026-09-01 07:18:00'
  ),
  (
    'run_delta_1',
    'delta.example',
    'no_change',
    0,
    '2026-09-01 08:00:00',
    '2026-09-01 08:09:00',
    540000,
    '',
    '',
    '2026-09-01 08:09:00',
    '2026-09-01 08:09:00'
  ),
  (
    'run_echo_1',
    'echo.example',
    'success',
    0,
    '2026-09-01 09:00:00',
    '2026-09-01 09:24:00',
    1440000,
    '',
    '',
    '2026-09-01 09:24:00',
    '2026-09-01 09:24:00'
  ),
  (
    'run_alpha_2',
    'alpha.example',
    'failed',
    0,
    '2026-09-01 10:00:00',
    '2026-09-01 10:26:00',
    1560000,
    'database',
    'pg_dump not available',
    '2026-09-01 10:26:00',
    '2026-09-01 10:26:00'
  ),
  (
    'run_gamma_2',
    'gamma.example',
    'success',
    0,
    '2026-09-01 11:00:00',
    '2026-09-01 11:23:00',
    1380000,
    '',
    '',
    '2026-09-01 11:23:00',
    '2026-09-01 11:23:00'
  );

INSERT INTO artifacts (
  id, run_id, source_kind, source_name, destination, object_key, size, sha256, status, error_message, created_at
) VALUES
  ('pkg_alpha_1', 'run_alpha_1', 'files', 'files', 'local-main', 'alpha.example/2026-09-01/files.tar.gz', 1048576, 'aaa111', 'stored', '', '2026-09-01 05:20:00'),
  ('pkg_beta_1', 'run_beta_1', 'files', 'files', 'local-main', 'beta.example/2026-09-01/files.tar.gz', 0, 'bbb222', 'failed', 'upload timeout', '2026-09-01 06:35:00'),
  ('pkg_gamma_1', 'run_gamma_1', 'files', 'files', 'local-main', 'gamma.example/2026-09-01/files.tar.gz', 2097152, 'ccc333', 'stored', '', '2026-09-01 07:18:00'),
  ('pkg_delta_1', 'run_delta_1', 'files', 'files', 'local-main', 'delta.example/2026-09-01/files.tar.gz', 524288, 'ddd444', 'stored', '', '2026-09-01 08:09:00'),
  ('pkg_echo_1', 'run_echo_1', 'files', 'files', 'local-main', 'echo.example/2026-09-01/files.tar.gz', 3145728, 'eee555', 'stored', '', '2026-09-01 09:24:00'),
  ('pkg_alpha_2', 'run_alpha_2', 'database', 'application-db', 'local-main', 'alpha.example/2026-09-01/application-db.sql.gz', 0, 'fff666', 'failed', 'database export failed', '2026-09-01 10:26:00'),
  ('pkg_gamma_2', 'run_gamma_2', 'files', 'files', 'local-main', 'gamma.example/2026-09-01/files.tar.gz', 1572864, 'ggg777', 'stored', '', '2026-09-01 11:23:00');

COMMIT;
