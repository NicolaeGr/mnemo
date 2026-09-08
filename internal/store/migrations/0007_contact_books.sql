-- Tags (books) on contacts. Invariant I1: every live contact has >= 1 row here.
CREATE TABLE contact_books (
    contact_id BIGINT NOT NULL REFERENCES contacts(id) ON DELETE CASCADE,
    book_id    BIGINT NOT NULL REFERENCES books(id) ON DELETE CASCADE,
    tagged_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (contact_id, book_id)
);
CREATE INDEX idx_contact_books_book ON contact_books(book_id);
