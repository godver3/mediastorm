-- +goose Up
-- Drop the ON DELETE CASCADE FK on users.account_id.
-- When an account is deleted, its profiles should become "unassigned" (orphaned),
-- not cascade-deleted. The users service manages this in-memory and the admin UI
-- displays profiles with a missing account as "Unassigned".
ALTER TABLE users DROP CONSTRAINT users_account_id_fkey;

-- +goose Down
ALTER TABLE users ADD CONSTRAINT users_account_id_fkey
    FOREIGN KEY (account_id) REFERENCES accounts(id) ON DELETE CASCADE;
