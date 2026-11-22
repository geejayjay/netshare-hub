-- +goose Up

-- Which computers may connect, as a JSON list of {"action": "allow" |
-- "block", "address": "<IP or CIDR range>"}. Always validated and
-- normalized by the service before it is stored.
ALTER TABLE shares ADD COLUMN network_rules TEXT NOT NULL DEFAULT '[]';

-- +goose Down
ALTER TABLE shares DROP COLUMN network_rules;
