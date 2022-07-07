package repository

import (
	"context"
	"fmt"
	"github.com/bianjieai/iobscan-ibc-explorer-backend/internal/app/model/dto"
	"github.com/bianjieai/iobscan-ibc-explorer-backend/internal/app/model/entity"
	"github.com/qiniu/qmgo"
	"go.mongodb.org/mongo-driver/bson"
	"time"
)

type IRelayerStatisticsRepo interface {
	InserOrUpdate(data entity.IBCRelayerStatistics) error
	CountRelayerBaseDenomAmt() ([]*dto.CountRelayerBaseDenomAmtDTO, error)
	Insert(relayerStatistics []entity.IBCRelayerStatistics) error
	AggregateRelayerTxs() ([]*dto.AggRelayerTxsDTO, error)
	CreateStatisticId(scChain, dcChain, scChannel, dcChannel string) (string, string)
}

var _ IRelayerStatisticsRepo = new(RelayerStatisticsRepo)

type RelayerStatisticsRepo struct {
}

func (repo *RelayerStatisticsRepo) CreateStatisticId(scChain, dcChain, scChannel, dcChannel string) (string, string) {
	return fmt.Sprintf("%s|%s|%s|%s", scChain, scChannel, dcChain, dcChannel), fmt.Sprintf("%s|%s|%s|%s", dcChain, dcChannel, scChain, scChannel)
}

//func (repo *RelayerStatisticsRepo) EnsureIndexes() {
//	var indexes []options.IndexModel
//	indexes = append(indexes, options.IndexModel{
//		Key:          []string{"-transfer_base_denom", "-relayer_id", "-chain_id", "-channel"},
//		IndexOptions: new(moptions.IndexOptions).SetUnique(true),
//	})
//	indexes = append(indexes, options.IndexModel{
//		Key: []string{"-relayer_id", "-chain_id"},
//	})
//
//	ensureIndexes(entity.IBCRelayerStatistics{}.CollectionName(), indexes)
//}

func (repo *RelayerStatisticsRepo) coll() *qmgo.Collection {
	return mgo.Database(ibcDatabase).Collection(entity.IBCRelayerStatistics{}.CollectionName())
}

func (repo *RelayerStatisticsRepo) Insert(relayerStatistics []entity.IBCRelayerStatistics) error {
	if _, err := repo.coll().InsertMany(context.Background(), relayerStatistics); err != nil {
		return err
	}
	return nil
}

func (repo *RelayerStatisticsRepo) InserOrUpdate(data entity.IBCRelayerStatistics) error {
	var res *entity.IBCRelayerStatistics
	filter := bson.M{
		"transfer_base_denom": data.TransferBaseDenom,
		"statistic_id":        data.StatisticId,
		"address":             data.Address,
		"segment_start_time":  data.SegmentStartTime,
		"segment_end_time":    data.SegmentEndTime,
	}
	err := repo.coll().Find(context.Background(), filter).One(&res)
	if err != nil {
		if err == qmgo.ErrNoSuchDocuments {
			if _, err := repo.coll().InsertOne(context.Background(), data); err != nil {
				return err
			}
			return nil
		}
		return err
	}
	return repo.coll().UpdateOne(context.Background(), filter,
		bson.M{
			"$set": bson.M{
				"transfer_amount":   data.TransferAmount,
				"success_total_txs": data.SuccessTotalTxs,
				"total_txs":         data.TotalTxs,
				"update_at":         time.Now().Unix(),
			},
		})
}

func (repo *RelayerStatisticsRepo) CountRelayerBaseDenomAmt() ([]*dto.CountRelayerBaseDenomAmtDTO, error) {
	match := bson.M{
		"$match": bson.M{},
	}
	group := bson.M{
		"$group": bson.M{
			"_id": bson.M{
				"address":      "$address",
				"statistic_id": "$statistic_id",
				"base_denom":   "$transfer_base_denom",
			},
			"amount": bson.M{
				"$sum": bson.M{"$toDouble": "$transfer_amount"},
			},
		},
	}
	project := bson.M{
		"$project": bson.M{
			"_id":          0,
			"address":      "$_id.address",
			"statistic_id": "$_id.statistic_id",
			"base_denom":   "$_id.base_denom",
			"amount":       "$amount",
		},
	}
	var pipe []bson.M
	pipe = append(pipe, match, group, project)
	var res []*dto.CountRelayerBaseDenomAmtDTO
	err := repo.coll().Aggregate(context.Background(), pipe).All(&res)
	return res, err
}

func (repo *RelayerStatisticsRepo) AggregateRelayerTxs() ([]*dto.AggRelayerTxsDTO, error) {
	match := bson.M{
		"$match": bson.M{},
	}
	group := bson.M{
		"$group": bson.M{
			"_id": bson.M{
				"address":      "$address",
				"statistic_id": "$statistic_id",
			},
			"total_txs": bson.M{
				"$sum": "$total_txs",
			},
			"success_total_txs": bson.M{
				"$sum": "$success_total_txs",
			},
		},
	}
	project := bson.M{
		"$project": bson.M{
			"_id":               0,
			"address":           "$_id.address",
			"statistic_id":      "$_id.statistic_id",
			"total_txs":         "$total_txs",
			"success_total_txs": "$success_total_txs",
		},
	}
	var pipe []bson.M
	pipe = append(pipe, match, group, project)
	var res []*dto.AggRelayerTxsDTO
	err := repo.coll().Aggregate(context.Background(), pipe).All(&res)
	return res, err
}
