CREATE TABLE ledger_currencies (
    code       text        PRIMARY KEY CHECK (code ~ '^[A-Z][A-Z0-9_]{2,15}$'),
    exponent   smallint    NOT NULL CHECK (exponent BETWEEN 0 AND 30),
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE TRIGGER ledger_currencies_immutable
    BEFORE UPDATE OR DELETE ON ledger_currencies
    FOR EACH ROW EXECUTE FUNCTION ledger_reject_mutation();

CREATE TRIGGER ledger_currencies_no_truncate
    BEFORE TRUNCATE ON ledger_currencies
    FOR EACH STATEMENT EXECUTE FUNCTION ledger_reject_mutation();

INSERT INTO ledger_currencies (code, exponent) VALUES
    ('AED', 2), ('AFN', 2), ('ALL', 2), ('AMD', 2), ('AOA', 2), ('ARS', 2), ('AUD', 2), ('AWG', 2), ('AZN', 2),
    ('BAM', 2), ('BBD', 2), ('BDT', 2), ('BGN', 2), ('BHD', 3), ('BIF', 0), ('BMD', 2), ('BND', 2), ('BOB', 2),
    ('BOV', 2), ('BRL', 2), ('BSD', 2), ('BTN', 2), ('BWP', 2), ('BYN', 2), ('BZD', 2), ('CAD', 2), ('CDF', 2),
    ('CHE', 2), ('CHF', 2), ('CHW', 2), ('CLF', 4), ('CLP', 0), ('CNY', 2), ('COP', 2), ('COU', 2), ('CRC', 2),
    ('CUP', 2), ('CVE', 2), ('CZK', 2), ('DJF', 0), ('DKK', 2), ('DOP', 2), ('DZD', 2), ('EGP', 2), ('ERN', 2),
    ('ETB', 2), ('EUR', 2), ('FJD', 2), ('FKP', 2), ('GBP', 2), ('GEL', 2), ('GHS', 2), ('GIP', 2), ('GMD', 2),
    ('GNF', 0), ('GTQ', 2), ('GYD', 2), ('HKD', 2), ('HNL', 2), ('HTG', 2), ('HUF', 2), ('IDR', 2), ('ILS', 2),
    ('INR', 2), ('IQD', 3), ('IRR', 2), ('ISK', 0), ('JMD', 2), ('JOD', 3), ('JPY', 0), ('KES', 2), ('KGS', 2),
    ('KHR', 2), ('KMF', 0), ('KPW', 2), ('KRW', 0), ('KWD', 3), ('KYD', 2), ('KZT', 2), ('LAK', 2), ('LBP', 2),
    ('LKR', 2), ('LRD', 2), ('LSL', 2), ('LYD', 3), ('MAD', 2), ('MDL', 2), ('MGA', 2), ('MKD', 2), ('MMK', 2),
    ('MNT', 2), ('MOP', 2), ('MRU', 2), ('MUR', 2), ('MVR', 2), ('MWK', 2), ('MXN', 2), ('MXV', 2), ('MYR', 2),
    ('MZN', 2), ('NAD', 2), ('NGN', 2), ('NIO', 2), ('NOK', 2), ('NPR', 2), ('NZD', 2), ('OMR', 3), ('PAB', 2),
    ('PEN', 2), ('PGK', 2), ('PHP', 2), ('PKR', 2), ('PLN', 2), ('PYG', 0), ('QAR', 2), ('RON', 2), ('RSD', 2),
    ('RUB', 2), ('RWF', 0), ('SAR', 2), ('SBD', 2), ('SCR', 2), ('SDG', 2), ('SEK', 2), ('SGD', 2), ('SHP', 2),
    ('SLE', 2), ('SOS', 2), ('SRD', 2), ('SSP', 2), ('STN', 2), ('SVC', 2), ('SYP', 2), ('SZL', 2), ('THB', 2),
    ('TJS', 2), ('TMT', 2), ('TND', 3), ('TOP', 2), ('TRY', 2), ('TTD', 2), ('TWD', 2), ('TZS', 2), ('UAH', 2),
    ('UGX', 0), ('USD', 2), ('USN', 2), ('UYI', 0), ('UYU', 2), ('UYW', 4), ('UZS', 2), ('VED', 2), ('VES', 2),
    ('VND', 0), ('VUV', 0), ('WST', 2), ('XAF', 0), ('XCD', 2), ('XCG', 2), ('XOF', 0), ('XPF', 0), ('YER', 2),
    ('ZAR', 2), ('ZMW', 2), ('ZWG', 2);

INSERT INTO ledger_currencies (code, exponent)
SELECT DISTINCT currency, 2 FROM ledger_accounts
ON CONFLICT (code) DO NOTHING;

ALTER TABLE ledger_holds DROP CONSTRAINT ledger_holds_account_id_currency_fkey;

ALTER TABLE ledger_accounts
    DROP CONSTRAINT ledger_accounts_currency_check,
    ALTER COLUMN currency TYPE text,
    ALTER COLUMN overdraft_limit TYPE numeric(38, 0),
    ALTER COLUMN balance TYPE numeric(38, 0),
    ALTER COLUMN held TYPE numeric(38, 0),
    ADD CONSTRAINT ledger_accounts_currency_fkey FOREIGN KEY (currency) REFERENCES ledger_currencies (code);

ALTER TABLE ledger_postings
    ALTER COLUMN currency TYPE text,
    ALTER COLUMN amount TYPE numeric(38, 0),
    ALTER COLUMN balance_after TYPE numeric(38, 0);

ALTER TABLE ledger_holds
    ALTER COLUMN currency TYPE text,
    ALTER COLUMN amount TYPE numeric(38, 0),
    ALTER COLUMN captured_amount TYPE numeric(38, 0),
    ADD CONSTRAINT ledger_holds_account_id_currency_fkey
        FOREIGN KEY (account_id, currency) REFERENCES ledger_accounts (id, currency);
