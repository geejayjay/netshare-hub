-- +goose Up

-- NFS export of a share (step 4.2), as JSON: enabled, mode ("user" or
-- "client-ids"), asUserId, clients [{address, readOnly}].
ALTER TABLE shares ADD COLUMN nfs TEXT NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE shares DROP COLUMN nfs;
