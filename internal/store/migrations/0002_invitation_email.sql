-- Invitations may target an email before that person has an account.
ALTER TABLE invitations ADD COLUMN invitee_email CITEXT;
CREATE INDEX idx_invitations_invitee_email
    ON invitations (invitee_email) WHERE invitee_email IS NOT NULL;
