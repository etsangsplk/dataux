package mongo

import (
	"database/sql/driver"
	"fmt"
	"github.com/araddon/qlbridge/expr"
	"sort"
	"strings"
	"sync"
	"time"

	"gopkg.in/mgo.v2"
	"gopkg.in/mgo.v2/bson"

	u "github.com/araddon/gou"
	"github.com/araddon/qlbridge/datasource"
	"github.com/araddon/qlbridge/value"
	"github.com/dataux/dataux/pkg/models"
)

var (
	// Ensure our MongoDataSource is a datasource.DataSource type
	_ datasource.DataSource = (*MongoDataSource)(nil)
	_ datasource.Scanner    = (*ResultReader)(nil)
	// source
	_ models.DataSource = (*MongoDataSource)(nil)
)

const (
	ListenerType = "mongo"
)

func init() {
	// We need to register our DataSource provider here
	models.DataSourceRegister("mongo", NewMongoDataSource)
}

// Mongo Data Source, is a singleton, non-threadsafe connection
//  to a backend mongo server
type MongoDataSource struct {
	db        string
	databases []string
	conf      *models.Config
	schema    *models.SourceSchema
	mu        sync.Mutex
	sess      *mgo.Session
	closed    bool
}

func NewMongoDataSource(schema *models.SourceSchema, conf *models.Config) models.DataSource {
	m := MongoDataSource{}
	m.schema = schema
	m.conf = conf
	m.db = strings.ToLower(schema.Db)
	// Register our datasource.Datasources in registry
	m.Init()
	datasource.Register("mongo", &m)
	return &m
}

func (m *MongoDataSource) Init() error {

	u.Infof("Init:  %#v", m.schema.Conf)
	if m.schema.Conf == nil {
		return fmt.Errorf("Schema conf not found")
	}

	// This will return an error if the database name we are using nis not found
	if err := m.connect(); err != nil {
		return err
	}

	if m.schema != nil {
		u.Debugf("Post Init() mongo schema P=%p tblct=%d", m.schema, len(m.schema.Tables))
	}

	return m.loadSchema()
}

func (m *MongoDataSource) loadSchema() error {

	if err := m.loadDatabases(); err != nil {
		u.Errorf("could not load mongo databases: %v", err)
		return err
	}

	if err := m.loadTableNames(); err != nil {
		u.Errorf("could not load mongo tables: %v", err)
		return err
	}
	return nil
}

func (m *MongoDataSource) Close() error {
	u.Infof("Closing MongoDataSource %p", m)
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.closed && m.sess != nil {
		u.Infof("Closing MongoDataSource %p session %p", m, m.sess)
		m.sess.Close()
	} else {
		u.Infof("Told to close mgomodelstore %p but session=%p closed=%t", m, m.closed)
	}
	m.closed = true
	return nil
}

func (m *MongoDataSource) connect() error {
	u.Infof("connecting MongoDataSource: %#v", m.schema.Conf)
	m.mu.Lock()
	defer m.mu.Unlock()

	// TODO
	//host := s.ChooseBackend()
	sess, err := mgo.Dial(m.schema.ChooseBackend())
	if err != nil {
		u.Errorf("Could not connect to mongo: %v", err)
		return err
	}

	// We depend on read-your-own-writes consistency in several places
	sess.SetMode(mgo.Strong, true)

	// Unbelievably, you have to actually enable error checking
	sess.SetSafe(&mgo.Safe{}) // copied from the mgo package docs

	sess.SetPoolLimit(1024)

	// Close the existing session if there is one
	if m.sess != nil {
		u.Infof("reconnecting MongoDataSource %p (closing old session)", m)
		m.sess.Close()
	}

	u.Infof("old session %p -> new session %p", m.sess, sess)
	m.sess = sess

	db := sess.DB(m.schema.Db)
	if db == nil {
		return fmt.Errorf("Database %v not found", m.schema.Db)
	}

	return nil
}

func (m *MongoDataSource) DataSource() datasource.DataSource {
	return m
}
func (m *MongoDataSource) Tables() []string {
	return m.schema.TableNames
}

func (m *MongoDataSource) Open(collectionName string) (datasource.SourceConn, error) {
	//u.Debugf("Open(%v)", collectionName)
	tbl := m.schema.Tables[collectionName]
	if tbl == nil {
		u.Errorf("Could not find table for '%s'.'%s'", m.schema.Db, collectionName)
		return nil, fmt.Errorf("Could not find '%v'.'%v' schema", m.schema.Db, collectionName)
	}

	es := NewSqlToMgo(tbl, m.sess)
	//u.Debugf("SqlToMgo: %#v", es)
	// resp, err := es.Query(stmt, m.sess)
	// if err != nil {
	// 	u.Error(err)
	// 	return nil, err
	// }
	return es, nil
}

/*
func (m *MongoDataSource) Accept(plan expr.SubVisitor) (datasource.Scanner, error) {

	u.Errorf("Accept %v", plan)
	// switch sql := plan.(type) {
	// case *expr.SqlSelect:
	// 	tblName := strings.ToLower(sql.From[0].Name)

	// 	tbl, _ := m.schema.Table(tblName)
	// 	if tbl == nil {
	// 		u.Errorf("Could not find table for '%s'.'%s'", m.schema.Db, tblName)
	// 		return nil, fmt.Errorf("Could not find '%v'.'%v' schema", m.schema.Db, tblName)
	// 	}

	// 	es := NewSqlToMgo(tbl)
	// 	u.Debugf("SqlToMgo: %#v", es)
	// 	resp, err := es.Query(sql, m.sess)
	// 	if err != nil {
	// 		u.Error(err)
	// 		return nil, err
	// 	}

	// 	return resp, nil
	// }
	return nil, fmt.Errorf("Not implemented")
}
*/
func (m *MongoDataSource) SourceTask(stmt *expr.SqlSelect) (models.SourceTask, error) {

	u.Debugf("get sourceTask for %v", stmt)
	tblName := strings.ToLower(stmt.From[0].Name)

	tbl := m.schema.Tables[tblName]
	if tbl == nil {
		u.Errorf("Could not find table for '%s'.'%s'", m.schema.Db, tblName)
		return nil, fmt.Errorf("Could not find '%v'.'%v' schema", m.schema.Db, tblName)
	}

	es := NewSqlToMgo(tbl, m.sess)
	u.Debugf("SqlToMgo: %#v", es)
	resp, err := es.Query(stmt)
	if err != nil {
		u.Error(err)
		return nil, err
	}

	return resp, nil
}

func (m *MongoDataSource) Table(table string) (*models.Table, error) {
	//u.Debugf("get table for %s", table)
	return m.loadTableSchema(table)
}

func (m *MongoDataSource) loadDatabases() error {

	dbs, err := m.sess.DatabaseNames()
	if err != nil {
		return err
	}
	sort.Strings(dbs)
	m.databases = dbs
	u.Debugf("found database names: %v", m.databases)
	found := false
	for _, db := range dbs {
		if strings.ToLower(db) == strings.ToLower(m.schema.Db) {
			found = true
		}
	}
	if !found {
		u.Warnf("could not find database: %v", m.schema.Db)
		return fmt.Errorf("Could not find that database: %v", m.schema.Db)
	}

	return nil
}

// Load only table/collection names, not full schema
func (m *MongoDataSource) loadTableNames() error {

	db := m.sess.DB(m.db)

	tables, err := db.CollectionNames()
	if err != nil {
		return err
	}
	sort.Strings(tables)
	m.schema.TableNames = tables
	u.Debugf("found tables: %v", m.schema.TableNames)
	return nil
}

func (m *MongoDataSource) loadTableSchema(table string) (*models.Table, error) {

	if m.schema == nil {
		return nil, fmt.Errorf("no schema in use")
	}
	// check cache first
	if tbl, ok := m.schema.Tables[table]; ok && tbl.Current() {
		return tbl, nil
	}

	/*
		TODO:
			- Need to read the indexes, and include that info
			- make recursive
			- Don't need ALL, just x rows
			- Resolve differences between object's? having different fields, types, nested
			- shared pkg for data-inspection, data builder
	*/
	tbl := models.NewTable(table, m.schema)
	coll := m.sess.DB(m.db).C(table)

	var dataType []map[string]interface{}
	if err := coll.Find(nil).All(&dataType); err != nil {
		u.Errorf("could not query collection")
	}
	for _, dt := range dataType {

		for colName, iVal := range dt {

			colName = strings.ToLower(colName)
			//u.Debugf("found col: %s %T=%v", colName, iVal, iVal)
			if tbl.HasField(colName) {
				continue
			}
			switch val := iVal.(type) {
			case bson.ObjectId:
				//u.Debugf("found bson.ObjectId: %v='%v'", colName, val)
				tbl.AddField(models.NewField(colName, value.StringType, 24, "bson.ObjectID AUTOGEN"))
				tbl.AddValues([]driver.Value{colName, "string", "NO", "PRI", "AUTOGEN", ""})
			case bson.M:
				//u.Debugf("found bson.M: %v='%v'", colName, val)
				tbl.AddField(models.NewField(colName, value.MapValueType, 24, "bson.M"))
				tbl.AddValues([]driver.Value{colName, "object", "NO", "", "", "Nested Map Type"})
			case map[string]interface{}:
				//u.Debugf("found map[string]interface{}: %v='%v'", colName, val)
				tbl.AddField(models.NewField(colName, value.MapValueType, 24, "map[string]interface{}"))
				tbl.AddValues([]driver.Value{colName, "object", "NO", "", "", "Nested Map Type"})
			case int:
				//u.Debugf("found int: %v='%v'", colName, val)
				tbl.AddField(models.NewField(colName, value.IntType, 32, "int"))
				tbl.AddValues([]driver.Value{colName, "int", "NO", "", "", "int"})
			case int64:
				//u.Debugf("found int64: %v='%v'", colName, val)
				tbl.AddField(models.NewField(colName, value.IntType, 32, "long"))
				tbl.AddValues([]driver.Value{colName, "long", "NO", "", "", "long"})
			case float64:
				//u.Debugf("found float64: %v='%v'", colName, val)
				tbl.AddField(models.NewField(colName, value.NumberType, 32, "float64"))
				tbl.AddValues([]driver.Value{colName, "float64", "NO", "", "", "float64"})
			case string:
				//u.Debugf("found string: %v='%v'", colName, val)
				tbl.AddField(models.NewField(colName, value.StringType, 32, "string"))
				tbl.AddValues([]driver.Value{colName, "string", "NO", "", "", "string"})
			case bool:
				//u.Debugf("found string: %v='%v'", colName, val)
				tbl.AddField(models.NewField(colName, value.BoolType, 1, "bool"))
				tbl.AddValues([]driver.Value{colName, "bool", "NO", "", "", "bool"})
			case time.Time:
				//u.Debugf("found time.Time: %v='%v'", colName, val)
				tbl.AddField(models.NewField(colName, value.TimeType, 32, "datetime"))
				tbl.AddValues([]driver.Value{colName, "datetime", "NO", "", "", "datetime"})
			case *time.Time:
				//u.Debugf("found time.Time: %v='%v'", colName, val)
				tbl.AddField(models.NewField(colName, value.TimeType, 32, "datetime"))
				tbl.AddValues([]driver.Value{colName, "datetime", "NO", "", "", "datetime"})
			case []uint8:
				// This is most likely binary data, json.RawMessage, or []bytes
				//u.Debugf("found []uint8: %v='%v'", colName, val)
				tbl.AddField(models.NewField(colName, value.ByteSliceType, 24, "[]byte"))
				tbl.AddValues([]driver.Value{colName, "binary", "NO", "", "", "Binary data:  []byte"})
			case []string:
				u.Warnf("NOT IMPLEMENTED:  found []string %v='%v'", colName, val)
			case []interface{}:
				//u.Debugf("SEMI IMPLEMENTED:   found []interface{}: %v='%v'", colName, val)
				typ := value.NilType
				for _, sliceVal := range val {
					typ = discoverType(sliceVal)
				}
				switch typ {
				case value.StringType:
					tbl.AddField(models.NewField(colName, value.StringsType, 24, "[]string"))
					tbl.AddValues([]driver.Value{colName, "[]string", "NO", "", "", "[]string"})
				default:
					u.Infof("SEMI IMPLEMENTED:   found []interface{}: %v='%v'  %T %v", colName, val, val, typ.String())
					tbl.AddField(models.NewField(colName, value.SliceValueType, 24, "[]value"))
					tbl.AddValues([]driver.Value{colName, "[]value", "NO", "", "", "[]value"})
				}

			default:
				u.Warnf("not recognized type: %v %T", colName, iVal)
			}
		}
	}

	// tbl.AddField(models.NewField("_id", value.StringType, 24, "AUTOGEN"))
	// tbl.AddField(models.NewField("type", value.StringType, 24, "tbd"))
	// tbl.AddField(models.NewField("_score", value.NumberType, 24, "Created per Search By Elasticsearch"))

	// tbl.AddValues([]driver.Value{"_id", "string", "NO", "PRI", "AUTOGEN", ""})
	// tbl.AddValues([]driver.Value{"type", "string", "NO", "", nil, "tbd"})
	// tbl.AddValues([]driver.Value{"_score", "float", "NO", "", nil, "Created per search"})

	// buildMongoFields(s, tbl, jh, "", 0)
	m.schema.Tables[table] = tbl

	return tbl, nil
}

func discoverType(iVal interface{}) value.ValueType {

	switch iVal.(type) {
	case bson.ObjectId:
		return value.StringType
	case bson.M:
		return value.MapValueType
	case map[string]interface{}:
		return value.MapValueType
	case int:
		return value.IntType
	case int64:
		return value.IntType
	case float64:
		return value.NumberType
	case string:
		return value.StringType
	case time.Time:
		return value.TimeType
	case *time.Time:
		return value.TimeType
	case []uint8:
		return value.ByteSliceType
	case []string:
		return value.StringsType
	case []interface{}:
		return value.SliceValueType
	default:
		u.Warnf("not recognized type:  %T", iVal)
	}
	return value.NilType
}

/*
func (m *MongoDataSource) addTypeInfo(tbl *models.Table, colName string, valType value.ValueType) {

	switch val := iVal.(type) {
	case bson.ObjectId:
		u.Debugf("found bson.ObjectId: %v='%v'", colName, val)
		tbl.AddField(models.NewField(colName, value.StringType, 24, "bson.ObjectID AUTOGEN"))
		tbl.AddValues([]driver.Value{colName, "string", "NO", "PRI", "AUTOGEN", ""})
	case bson.M:
		u.Debugf("found bson.M: %v='%v'", colName, val)
		tbl.AddField(models.NewField(colName, value.MapValueType, 24, "bson.M"))
		tbl.AddValues([]driver.Value{colName, "object", "NO", "", "", "Nested Map Type"})
	case map[string]interface{}:
		u.Debugf("found map[string]interface{}: %v='%v'", colName, val)
		tbl.AddField(models.NewField(colName, value.MapValueType, 24, "map[string]interface{}"))
		tbl.AddValues([]driver.Value{colName, "object", "NO", "", "", "Nested Map Type"})
	case int:
		u.Debugf("found int: %v='%v'", colName, val)
		tbl.AddField(models.NewField(colName, value.IntType, 32, "int"))
		tbl.AddValues([]driver.Value{colName, "int", "NO", "", "", "int"})
	case int64:
		u.Debugf("found int64: %v='%v'", colName, val)
		tbl.AddField(models.NewField(colName, value.IntType, 32, "long"))
		tbl.AddValues([]driver.Value{colName, "long", "NO", "", "", "long"})
	case float64:
		u.Debugf("found float64: %v='%v'", colName, val)
		tbl.AddField(models.NewField(colName, value.NumberType, 32, "float64"))
		tbl.AddValues([]driver.Value{colName, "float64", "NO", "", "", "float64"})
	case string:
		u.Debugf("found string: %v='%v'", colName, val)
		tbl.AddField(models.NewField(colName, value.StringType, 32, "string"))
		tbl.AddValues([]driver.Value{colName, "string", "NO", "", "", "string"})
	case time.Time:
		u.Debugf("found time.Time: %v='%v'", colName, val)
		tbl.AddField(models.NewField(colName, value.TimeType, 32, "datetime"))
		tbl.AddValues([]driver.Value{colName, "datetime", "NO", "", "", "datetime"})
	case *time.Time:
		u.Debugf("found time.Time: %v='%v'", colName, val)
		tbl.AddField(models.NewField(colName, value.TimeType, 32, "datetime"))
		tbl.AddValues([]driver.Value{colName, "datetime", "NO", "", "", "datetime"})
	case []uint8:
		// This is most likely binary data, json.RawMessage, or []bytes
		u.Debugf("found []uint8: %v='%v'", colName, val)
		tbl.AddField(models.NewField(colName, value.ByteSliceType, 24, "[]byte"))
		tbl.AddValues([]driver.Value{colName, "binary", "NO", "", "", "Binary data:  []byte"})
	case []string:
		u.Warnf("NOT IMPLEMENTED:  found []string %v='%v'", colName, val)
	case []interface{}:
		u.Infof("NOT IMPLEMENTED:   found []interface{}: %v='%v'", colName, val)
		typ := value.NilType
		for _, sliceVal := range val {

		}
		tbl.AddField(models.NewField(colName, value.ByteSliceType, 24, "[]byte"))
		tbl.AddValues([]driver.Value{colName, "binary", "NO", "", "", "Binary data:  []byte"})
	default:
		u.Warnf("not recognized type: %v %T", colName, iVal)
	}
}
*/
