ALTER TYPE task_status ADD VALUE 'cancelled' AFTER 'archived';

ALTER TYPE task_status ADD VALUE 'restored' AFTER 'cancelled';
