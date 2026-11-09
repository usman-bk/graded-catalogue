CREATE TABLE IF NOT EXISTS products (
    id      text PRIMARY KEY,
    title   text NOT NULL,
    brand   text NOT NULL,
    model   text NOT NULL,
    storage text NOT NULL DEFAULT '',
    colour  text NOT NULL DEFAULT ''
);

CREATE TABLE IF NOT EXISTS sellers (
    id            text   PRIMARY KEY,
    name          text   NOT NULL,
    rating        real   NOT NULL DEFAULT 0,
    rating_count  int    NOT NULL DEFAULT 0,
    dispatch_days int    NOT NULL DEFAULT 1
);

-- One row is one physical unit. There is no quantity column, and that is
-- the schema stating the thing the whole service is about: a quantity
-- column invites "UPDATE ... SET qty = qty - 1" and every bug that comes
-- with it, on a table where the number is always one.
CREATE TABLE IF NOT EXISTS offers (
    id              text        PRIMARY KEY,
    product_id      text        NOT NULL REFERENCES products(id),
    seller_id       text        NOT NULL REFERENCES sellers(id),
    grade           smallint    NOT NULL CHECK (grade BETWEEN 1 AND 3),
    price_cents     bigint      NOT NULL CHECK (price_cents > 0),
    currency        char(3)     NOT NULL,
    battery_pct     smallint    NOT NULL DEFAULT 0 CHECK (battery_pct BETWEEN 0 AND 100),
    warranty_months smallint    NOT NULL DEFAULT 0,
    state           text        NOT NULL DEFAULT 'available'
                                CHECK (state IN ('available','reserved','sold','withdrawn')),
    reserved_at     timestamptz,
    reserved_by     text,
    reserved_until  timestamptz,
    listed_at       timestamptz NOT NULL DEFAULT now(),

    -- A reserved row must say who holds it and until when, and a row in any
    -- other state must not. Without this the expiry sweep has to guess what
    -- a reserved row with no deadline means, and the answer people reach for
    -- is "leave it", which takes the unit off the market permanently.
    CONSTRAINT reservation_is_complete CHECK (
        (state = 'reserved' AND reserved_by IS NOT NULL AND reserved_until IS NOT NULL)
        OR (state <> 'reserved' AND reserved_until IS NULL)
    )
);

-- The buy box reads every available offer for one product, which is the
-- hottest query here.
CREATE INDEX IF NOT EXISTS offers_product_available_idx
    ON offers (product_id, price_cents) WHERE state = 'available';

-- The expiry sweep looks for reservations past their deadline. Partial,
-- because reserved rows are a small fraction of the table and a full index
-- would be mostly dead weight.
CREATE INDEX IF NOT EXISTS offers_reserved_until_idx
    ON offers (reserved_until) WHERE state = 'reserved';
