-- Local development credentials only.
-- These roles must remain separate from the administrative account.

CREATE ROLE wagering_migrator
    LOGIN
    PASSWORD 'wagering_migrator_local'
    NOSUPERUSER
    NOCREATEDB
    NOCREATEROLE
    NOREPLICATION;

CREATE ROLE wagering_app
    LOGIN
    PASSWORD 'wagering_app_local'
    NOSUPERUSER
    NOCREATEDB
    NOCREATEROLE
    NOREPLICATION;

REVOKE ALL ON DATABASE wagering FROM PUBLIC;

GRANT CONNECT ON DATABASE wagering
    TO wagering_migrator, wagering_app;

REVOKE ALL ON SCHEMA public FROM PUBLIC;

CREATE SCHEMA wagering AUTHORIZATION wagering_migrator;

GRANT USAGE ON SCHEMA wagering TO wagering_app;

ALTER ROLE wagering_migrator IN DATABASE wagering
    SET search_path TO wagering, pg_catalog;

ALTER ROLE wagering_app IN DATABASE wagering
    SET search_path TO wagering, pg_catalog;