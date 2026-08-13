CREATE TABLE agent_endpoint_profiles (
    id uuid PRIMARY KEY,
    name text NOT NULL,
    registration_url text NOT NULL,
    quic_address text NOT NULL,
    registration_ca_certificate_pem text NOT NULL DEFAULT '',
    enabled boolean NOT NULL DEFAULT true,
    revision bigint NOT NULL DEFAULT 1 CHECK (revision > 0),
    created_by_user_id uuid,
    updated_by_user_id uuid,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT agent_endpoint_profiles_name_format CHECK (
        name = btrim(name) AND octet_length(name) BETWEEN 1 AND 128
    ),
    CONSTRAINT agent_endpoint_profiles_name_unique UNIQUE (name)
);

CREATE UNIQUE INDEX agent_endpoint_profiles_name_case_insensitive_unique
    ON agent_endpoint_profiles (lower(name));

INSERT INTO agent_endpoint_profiles (
    id,
    name,
    registration_url,
    quic_address,
    enabled
) VALUES (
    '00000000-0000-0000-0000-000000000010',
    '本机回环预览',
    'http://127.0.0.1:8080',
    '127.0.0.1:8443',
    true
), (
    '00000000-0000-0000-0000-000000000011',
    'Docker Desktop / OrbStack',
    'http://host.docker.internal:8080',
    'host.docker.internal:8443',
    true
);

CREATE TABLE platform_settings (
    singleton boolean PRIMARY KEY DEFAULT true CHECK (singleton),
    default_endpoint_profile_id uuid NOT NULL REFERENCES agent_endpoint_profiles (id),
    agent_image text NOT NULL,
    agent_namespace text NOT NULL,
    agent_image_pull_policy text NOT NULL,
    cluster_terminal_image text NOT NULL,
    revision bigint NOT NULL DEFAULT 1 CHECK (revision > 0),
    updated_by_user_id uuid,
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT platform_settings_agent_image_format CHECK (
        agent_image = btrim(agent_image) AND octet_length(agent_image) BETWEEN 1 AND 512
    ),
    CONSTRAINT platform_settings_agent_namespace_format CHECK (
        agent_namespace = btrim(agent_namespace) AND octet_length(agent_namespace) BETWEEN 1 AND 63
    ),
    CONSTRAINT platform_settings_terminal_image_format CHECK (
        cluster_terminal_image = btrim(cluster_terminal_image)
        AND octet_length(cluster_terminal_image) BETWEEN 1 AND 512
    ),
    CONSTRAINT platform_settings_pull_policy CHECK (
        agent_image_pull_policy IN ('Always', 'IfNotPresent', 'Never')
    )
);

INSERT INTO platform_settings (
    default_endpoint_profile_id,
    agent_image,
    agent_namespace,
    agent_image_pull_policy,
    cluster_terminal_image
) VALUES (
    '00000000-0000-0000-0000-000000000010',
    'ghcr.io/togettoyou/zke-agent:latest',
    'zke-system',
    'IfNotPresent',
    'ghcr.io/togettoyou/zke-agent:latest'
);

ALTER TABLE enrollments
    ADD COLUMN endpoint_profile_id uuid,
    ADD COLUMN endpoint_profile_revision bigint,
    ADD COLUMN registration_url text,
    ADD COLUMN quic_address text,
    ADD COLUMN registration_ca_certificate_pem text,
    ADD COLUMN agent_image text,
    ADD COLUMN agent_namespace text,
    ADD COLUMN agent_image_pull_policy text;

UPDATE enrollments
SET
    endpoint_profile_id = '00000000-0000-0000-0000-000000000010',
    endpoint_profile_revision = 1,
    registration_url = 'http://127.0.0.1:8080',
    quic_address = '127.0.0.1:8443',
    registration_ca_certificate_pem = '',
    agent_image = 'ghcr.io/togettoyou/zke-agent:latest',
    agent_namespace = 'zke-system',
    agent_image_pull_policy = 'IfNotPresent';

ALTER TABLE enrollments
    ALTER COLUMN endpoint_profile_id SET NOT NULL,
    ALTER COLUMN endpoint_profile_revision SET NOT NULL,
    ALTER COLUMN registration_url SET NOT NULL,
    ALTER COLUMN quic_address SET NOT NULL,
    ALTER COLUMN registration_ca_certificate_pem SET NOT NULL,
    ALTER COLUMN agent_image SET NOT NULL,
    ALTER COLUMN agent_namespace SET NOT NULL,
    ALTER COLUMN agent_image_pull_policy SET NOT NULL,
    ADD CONSTRAINT enrollments_endpoint_profile_revision_check
        CHECK (endpoint_profile_revision > 0),
    ADD CONSTRAINT enrollments_agent_image_pull_policy_check
        CHECK (agent_image_pull_policy IN ('Always', 'IfNotPresent', 'Never'));
