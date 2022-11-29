package repository

import (
	"context"
	"time"

	"github.com/bianjieai/iobscan-ibc-explorer-backend/internal/app/model/dto"
	"github.com/bianjieai/iobscan-ibc-explorer-backend/internal/app/model/entity"
	"github.com/qiniu/qmgo"
	"go.mongodb.org/mongo-driver/bson"
)

type ITokenTraceRepo interface {
	DelByBaseDenom(baseDenom, BaseDenomChain string) error
	BatchSwap(batch []*entity.IBCTokenTrace, baseDenom, BaseDenomChain string) error
	AggregateIBCChain() ([]*dto.AggregateIBCChainDTO, error)
}

var _ ITokenTraceRepo = new(TokenTraceRepo)

type TokenTraceRepo struct {
}

func (repo *TokenTraceRepo) coll() *qmgo.Collection {
	return mgo.Database(ibcDatabase).Collection(entity.IBCTokenTrace{}.CollectionName())
}

func (repo *TokenTraceRepo) DelByBaseDenom(baseDenom, BaseDenomChain string) error {
	query := bson.M{
		"base_denom":       baseDenom,
		"base_denom_chain": BaseDenomChain,
	}
	_, err := repo.coll().RemoveAll(context.Background(), query)
	return err
}

func (repo *TokenTraceRepo) BatchSwap(batch []*entity.IBCTokenTrace, baseDenom, BaseDenomChain string) error {
	callback := func(sessCtx context.Context) (interface{}, error) {
		query := bson.M{
			"base_denom":       baseDenom,
			"base_denom_chain": BaseDenomChain,
		}
		if _, err := repo.coll().RemoveAll(sessCtx, query); err != nil {
			return nil, err
		}

		if len(batch) == 0 {
			return nil, nil
		}

		for _, v := range batch {
			v.CreateAt = time.Now().Unix()
			v.UpdateAt = time.Now().Unix()
		}
		if _, err := repo.coll().InsertMany(sessCtx, batch); err != nil {
			return nil, err
		}

		return nil, nil
	}
	_, err := mgo.DoTransaction(context.Background(), callback)
	return err
}

func (repo *TokenTraceRepo) AggregateIBCChain() ([]*dto.AggregateIBCChainDTO, error) {
	match := bson.M{
		"$match": bson.M{
			"type": bson.M{
				"$in": []entity.TokenTraceType{entity.TokenTraceTypeAuthed, entity.TokenTraceTypeOther},
			},
		},
	}

	group := bson.M{
		"$group": bson.M{
			"_id": "$chain",
			"denom_value": bson.M{
				"$sum": bson.M{
					"$toDouble": "$denom_value",
				},
			},
			"Count": bson.M{
				"$sum": 1,
			},
		},
	}

	var pipe []bson.M
	pipe = append(pipe, match, group)
	var res []*dto.AggregateIBCChainDTO
	err := repo.coll().Aggregate(context.Background(), pipe).All(&res)
	return res, err
}
