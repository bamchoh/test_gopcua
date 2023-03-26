import React from 'react';
import { useEffect, useState } from 'react';

function Home() {
  const [items, setItems] = useState([])

  useEffect(() => {
    fetch(process.env.REACT_APP_HOST_URL + '/api/alarms')
    .then((response) => {
      return response.body
    })
    .then((rb) => {
      const reader = rb?.getReader();

      return new ReadableStream({
        start(controller) {
          function push() {
            reader?.read().then(({done, value}) => {
              if(done) {
                console.log("done")
                controller.close()
                return;
              }

              var jsonData = JSON.parse(new TextDecoder().decode(value))

              setItems(jsonData)

              push();
            });
          }

          push();
        }
      })
    })
  }, [])

  return (
    <>
    <h2>Home</h2>
    <button className="bg-red-500 text-white font-bold py-2 px-4 ">
      Hello Tailwind
    </button>    
    <table className="text-sm text-left text-gray-500 dark:text-gray-400">
      <thead className="text-xs text-gray-700 uppercase bg-gray-50 dark:bg-gray-700 dark:text-gray-400">
        <tr>
          <th className="px-6 py-3" >コード</th>
          <th className="px-6 py-3" >メッセージ</th>
          <th className="px-6 py-3" >発生時刻</th>
          <th className="px-6 py-3" >値</th>
        </tr>
      </thead>
      <tbody>
        {items.map((item:any, index) => (
          <tr key={item.id} className="bg-white border-b dark:bg-gray-900 dark:border-gray-700">
            <td className="px-6 py-4" >{item.code}</td>
            <td className="px-6 py-4" >{item.message}</td>
            <td className="px-6 py-4" >{(new Date(item.timestamp * 1000)).toISOString()}</td>
            <td className="px-6 py-4" >{item.value}</td>
          </tr>
        ))}
      </tbody>
    </table>
    </>
  )
}

function App() {
  return (
    <div className="App">
      <Home />
    </div>
  );
}

export default App;
