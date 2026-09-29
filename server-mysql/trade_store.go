package main

import "fmt"

// SaveTradeExchange commits both players' new bag and coin snapshots in one
// MySQL transaction. No other player state is replaced by a trade.
func (st *Store) SaveTradeExchange(firstID int64, firstCoin int64, firstBag map[int32]*bagItem,
	secondID int64, secondCoin int64, secondBag map[int32]*bagItem) error {
	if st == nil {
		return nil
	}
	if firstID <= 0 || secondID <= 0 || firstID == secondID {
		return fmt.Errorf("invalid trade players %d/%d", firstID, secondID)
	}
	st.persistMu.Lock()
	defer st.persistMu.Unlock()

	tx, err := st.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	rows, err := tx.Query(`SELECT id FROM players WHERE id IN (?, ?) ORDER BY id FOR UPDATE`, firstID, secondID)
	if err != nil {
		return err
	}
	locked := 0
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		locked++
	}
	if err := closeRows(rows); err != nil {
		return err
	}
	if locked != 2 {
		return fmt.Errorf("trade players disappeared: locked %d", locked)
	}
	for _, update := range []struct {
		id   int64
		coin int64
		bag  map[int32]*bagItem
	}{
		{id: firstID, coin: firstCoin, bag: firstBag},
		{id: secondID, coin: secondCoin, bag: secondBag},
	} {
		if _, err := tx.Exec(`UPDATE players SET coin = ? WHERE id = ?`, update.coin, update.id); err != nil {
			return err
		}
		if _, err := tx.Exec(`DELETE FROM player_items WHERE player_id = ? AND location = ?`, update.id, itemLocationBag); err != nil {
			return err
		}
		if err := saveItemsTx(tx, update.id, itemLocationBag, update.bag); err != nil {
			return err
		}
	}
	return tx.Commit()
}
